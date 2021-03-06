package main

import (
	"encoding/json"
	"fmt"
	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/types/current"
	"github.com/containernetworking/cni/pkg/version"
	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
	_ "github.com/containernetworking/plugins/pkg/utils/sysctl"
	"github.com/vishvananda/netlink"
	"log"
	"net"
	"runtime"
	"strings"
	"ts-cni/cni/structs"
	"ts-cni/cni/utils"
)

type EnvArgs struct {
	types.CommonArgs
	K8sPodNamespace        string `json:"K8S_POD_NAMESPACE"`
	K8sPodName             string `json:"K8S_POD_NAME"`
	K8sPodInfraContainerId string `json:"K8S_POD_INFRA_CONTAINER_ID"`
}

type NetConf struct {
	types.NetConf
	Master  string `json:"master"`
	Mode    string `json:"mode"`
	MTU     int    `json:"mtu"`
	Mac     string `json:"mac,omitempty"`
	EnvArgs EnvArgs
	NetInfo structs.NetInfo
}

//const (
//	IPv4InterfaceArpProxySysctlTemplate = "net.ipv4.conf.%s.proxy_arp"
//)

func init() {
	log.SetPrefix("TC-CNI: ")
	log.SetFlags(log.Ldate | log.Lmicroseconds | log.Lshortfile)
	runtime.LockOSThread()
}

func getDefaultRouteInterfaceName() (string, error) {
	routeToDstIP, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return "", err
	}

	for _, v := range routeToDstIP {
		if v.Dst == nil {
			l, err := netlink.LinkByIndex(v.LinkIndex)
			if err != nil {
				return "", err
			}
			return l.Attrs().Name, nil
		}
	}

	return "", fmt.Errorf("no default route interface found")
}

func getMTUByName(ifName string) (int, error) {
	link, err := netlink.LinkByName(ifName)
	if err != nil {
		return 0, err
	}
	return link.Attrs().MTU, nil
}

func modeFromString(s string) (netlink.MacvlanMode, error) {
	switch s {
	case "", "bridge":
		return netlink.MACVLAN_MODE_BRIDGE, nil
	case "private":
		return netlink.MACVLAN_MODE_PRIVATE, nil
	case "vepa":
		return netlink.MACVLAN_MODE_VEPA, nil
	case "passthru":
		return netlink.MACVLAN_MODE_PASSTHRU, nil
	default:
		return 0, fmt.Errorf("unknown macvlan mode: %q", s)
	}
}

func loadConf(bytes []byte, envArgs string) (*NetConf, string, error) {
	n := &NetConf{}
	if err := json.Unmarshal(bytes, n); err != nil {
		return nil, "", fmt.Errorf("failed to load netconf: %v", err)
	}
	log.Println("CNI LoadConf???n:", *n)
	log.Println("CNI LoadConf???envArgs:", envArgs)
	// ?????????????????????????????????
	// ???????????????????????????=
	// IgnoreUnknown=1;
	// K8S_POD_NAMESPACE=default;
	// K8S_POD_NAME=nginx-test;
	// K8S_POD_INFRA_CONTAINER_ID=c9955ddd4f37e4822f4ddb198e1c4069fa4598720897a07b68f4114267285c12
	if envArgs != "" {
		log.Println("CNI envArgs???????????????=", envArgs)
		//2021-07-30 13:31:17.590884 I | TC-CNI: 2021/07/30 13:31:17.590874 tc-cni.go:105:
		//CNI envArgs???????????????= IgnoreUnknown=1;
		//K8S_POD_NAMESPACE=default;
		//K8S_POD_NAME=nginx-test-5f6cc55c7f-jmrqf;
		//K8S_POD_INFRA_CONTAINER_ID=106974fef45160ef276ce66499f9570cbe0b56a209ed4ead3857b5277bb2da5c
		m := &EnvArgs{}
		/*
			type EnvArgs struct {
				types.CommonArgs
				K8sPodNamespace        string `json:"K8S_POD_NAMESPACE"`
				K8sPodName             string `json:"K8S_POD_NAME"`
				K8sPodInfraContainerId string `json:"K8S_POD_INFRA_CONTAINER_ID"`
			}

			type CommonArgs struct {
				IgnoreUnknown UnmarshallableBool `json:"ignoreunknown,omitempty"`
			}
		*/
		tempMap := make(map[string]string)
		tempArr := strings.Split(envArgs, ";")
		for _, v := range tempArr {
			tempKV := strings.Split(v, "=")
			tempMap[tempKV[0]] = tempKV[1]
		}
		if tempMap["IgnoreUnknown"] == "1" {
			m.IgnoreUnknown = true
		} else {
			m.IgnoreUnknown = false
		}
		m.K8sPodNamespace = tempMap["K8S_POD_NAMESPACE"]
		m.K8sPodName = tempMap["K8S_POD_NAME"]
		m.K8sPodInfraContainerId = tempMap["K8sPodInfraContainerId"]
		n.EnvArgs = *m
		log.Println("CNI envArgs???????????????=", *m)
		log.Println("CNI ?????????n??????=", *n)
	}

	// ???????????????????????????????????????
	if n.Master == "" {
		defaultRouteInterface, err := getDefaultRouteInterfaceName()
		if err != nil {
			return nil, "", err
		}
		n.Master = defaultRouteInterface
	}
	// ??????k8s??????
	K8sClient := utils.NewK8s()
	// ??????n.EnvArgs???????????????pod???namespace???name??????????????????????????????????????????app_net??????
	log.Println("??????pod namespace =", n.EnvArgs.K8sPodNamespace)
	log.Println("??????pod????????? =", n.EnvArgs.K8sPodName)
	netArr := K8sClient.GetPodNet(n.EnvArgs.K8sPodNamespace, n.EnvArgs.K8sPodName)
	log.Println("CNI NetArr??????=", netArr)
	ipInfo := utils.EtcdCmdAdd(netArr)
	log.Println("CNI IpInfo=", ipInfo)
	n.Master = n.Master + "." + ipInfo.VlanId
	n.NetInfo.AppNet = ipInfo.AppNet
	n.NetInfo.UseIpList = ipInfo.UseIpList
	n.NetInfo.IPAddress = ipInfo.IPAddress
	n.NetInfo.GateWay = ipInfo.GateWay
	log.Println("CNI IP????????????=", n.Master, n.NetInfo)
	log.Println("CNI IP????????????n?????????=", n)

	// ??????etcd??????
	//etcdClient := utils.EtcdClient{}
	//etcdClient.EtcdConnect()
	// ???etcd???????????????????????????
	// ??????Annotations??????app_net
	// ????????? ??????app_net?????????etcd?????????????????????
	// ????????? ??????????????????app_net??????????????????IP???etcd??????????????????240???(11-250)
	// ????????? ????????????????????????app_net???????????????????????????????????????????????????????????????IP??????
	// ????????? ???????????????240????????????etcd?????????????????????vlanID??????????????????master???
	//var etcdRootDir = "/ipam"
	//netList := etcdClient.EtcdGet(etcdRootDir, true).([]string)
	//for i, v := range netArr {
	//	if utils.IsExistString(v, netList) {
	//		usedIpList := etcdClient.EtcdGet(etcdRootDir+"/"+v, true).([]string)
	//		if len(usedIpList) < 240 {
	//			netVlanId := etcdClient.EtcdGet(etcdRootDir+"/"+v, false).([]utils.EtcdGetValue)[0].V
	//			n.Master = n.Master + "." + netVlanId
	//			n.NetInfo.AppNet = v
	//			n.NetInfo.UseIpList = usedIpList
	//			break
	//		} else if i == len(usedIpList)-1 {
	//			return nil, "", fmt.Errorf("%v ???Annotations??????app_net??????????????????! \n")
	//		} else {
	//			log.Printf("%v ??????IP????????????????????????IP???! \n", v)
	//			continue
	//		}
	//	} else {
	//		return nil, "", fmt.Errorf("%v ???Annotations??????app_net???????????????????????????! \n", n.EnvArgs.K8sPodName)
	//	}
	//}
	//etcdClient.EtcdDisconnect()

	// ??????MTU?????????????????????????????????0 ??????????????????
	masterMTU, err := getMTUByName(n.Master)
	if err != nil {
		return nil, "", err
	}
	if n.MTU < 0 || n.MTU > masterMTU {
		return nil, "", fmt.Errorf("invalid MTU %d, must be [0, master MTU(%d)]", n.MTU, masterMTU)
	}

	return n, n.CNIVersion, nil
}

func createMacvlan(conf *NetConf, ifName string, netns ns.NetNS) (*current.Interface, error) {
	macvlan := &current.Interface{}
	// ????????????????????????mode
	mode, err := modeFromString(conf.Mode)
	if err != nil {
		return nil, err
	}
	log.Println("CNI macvlan???mode=", mode)

	m, err := netlink.LinkByName(conf.Master)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup master %q: %v", conf.Master, err)
	}
	log.Println("CNI macvlan???m=", m)

	// ??????????????????
	// due to kernel bug we have to create with tmpName or it might
	// collide with the name on the host and error out
	tmpName, err := ip.RandomVethName()
	if err != nil {
		return nil, err
	}

	linkAttrs := netlink.LinkAttrs{
		MTU:         conf.MTU,
		Name:        tmpName,
		ParentIndex: m.Attrs().Index,
		Namespace:   netlink.NsFd(int(netns.Fd())),
	}

	if conf.Mac != "" {
		addr, err := net.ParseMAC(conf.Mac)
		if err != nil {
			return nil, fmt.Errorf("invalid args %v for MAC addr: %v", conf.Mac, err)
		}
		linkAttrs.HardwareAddr = addr
	}
	log.Println("CNI linkAttrs.HardwareAddr??????=", linkAttrs.HardwareAddr)
	log.Println("CNI linkAttrs??????=")

	// ??????macvlan??????????????????
	mv := &netlink.Macvlan{
		LinkAttrs: linkAttrs,
		Mode:      mode,
	}

	if err := netlink.LinkAdd(mv); err != nil {
		return nil, fmt.Errorf("failed to create macvlan: %v", err)
	}

	err = netns.Do(func(_ ns.NetNS) error {
		// TODO: duplicate following lines for ipv6 support, when it will be added in other places
		//ipv4SysctlValueName := fmt.Sprintf(IPv4InterfaceArpProxySysctlTemplate, tmpName)
		//if _, err := sysctl.Sysctl(ipv4SysctlValueName, "1"); err != nil {
		//	// remove the newly added link and ignore errors, because we already are in a failed state
		//	_ = netlink.LinkDel(mv)
		//	return fmt.Errorf("failed to set proxy_arp on newly added interface %q: %v", tmpName, err)
		//}

		err := ip.RenameLink(tmpName, ifName)
		// ????????????????????????????????????????????????????????? --- ??????
		if err != nil {
			_ = netlink.LinkDel(mv)
			return fmt.Errorf("failed to rename macvlan to %q: %v", ifName, err)
		}
		macvlan.Name = ifName

		// Re-fetch macvlan to get all properties/attributes
		contMacvlan, err := netlink.LinkByName(ifName)
		log.Println("contMacvlan??????=", contMacvlan)
		if err != nil {
			return fmt.Errorf("failed to refetch macvlan %q: %v", ifName, err)
		}
		macvlan.Mac = contMacvlan.Attrs().HardwareAddr.String()
		macvlan.Sandbox = netns.Path()

		return nil
	})
	if err != nil {
		return nil, err
	}
	log.Println("CNI macvlan??????=", *macvlan)
	// {eth0 0e:69:d6:07:a9:33 /proc/25981/ns/net}
	return macvlan, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	log.Println("CNI Args??????=", *args)
	n, cniVersion, err := loadConf(args.StdinData, args.Args)
	if err != nil {
		return err
	}
	log.Println("CNI ??????????????????n=", n)

	// ??????????????????
	netns, err := ns.GetNS(args.Netns)
	if err != nil {
		return fmt.Errorf("failed to open netns %q: %v", netns, err)
	}
	log.Println("CNI ??????????????????netns=", netns)
	defer netns.Close()

	// macvlanInterface????????? {eth0 82:e1:18:79:a4:5d /proc/10491/ns/net}
	macvlanInterface, err := createMacvlan(n, args.IfName, netns)
	if err != nil {
		return err
	}
	log.Println("macvlanInterface??????=", *macvlanInterface)

	// Delete link if err to avoid link leak in this ns
	defer func() {
		if err != nil {
			netns.Do(func(_ ns.NetNS) error {
				return ip.DelLinkByName(args.IfName)
			})
		}
	}()

	result := &current.Result{
		CNIVersion: cniVersion,
		Interfaces: []*current.Interface{macvlanInterface},
	}

	// ???????????? ?????????
	/*
		//isLayer3 := n.IPAM.Type != ""
		//if isLayer3 {
		//	// run the IPAM plugin and get back the config to apply
		//	r, err := ipam.ExecAdd(n.IPAM.Type, args.StdinData)
		//	log.Println("r??????:", r)
		//	// r??????: &{0.3.1 [] [{Version:4 Interface:<nil> Address:{IP:192.168.165.22 Mask:ffffff00} Gateway:192.168.165.2}]
		//	//[{Dst:{IP:0.0.0.0 Mask:00000000} GW:<nil>}] {[]  [] []}}
		//	if err != nil {
		//		return err
		//	}
		//
		//	// Invoke ipam del if err to avoid ip leak
		//	defer func() {
		//		if err != nil {
		//			ipam.ExecDel(n.IPAM.Type, args.StdinData)
		//		}
		//	}()
		//
		//	// Convert whatever the IPAM result was into the current Result type
		//	ipamResult, err := current.NewResultFromResult(r)
		//	log.Println("ipamResult??????:", *ipamResult)
		//	// ipamResult??????: {0.4.0 [] [{Version:4 Interface:<nil> Address:{IP:192.168.165.22 Mask:ffffff00} Gateway:192.168.165.2}]
		//	//[{Dst:{IP:0.0.0.0 Mask:00000000} GW:<nil>}] {[]  [] []}}
		//	// CNIVersion 0.4.0
		//	// Interfaces []
		//	// IPs [{Version:4 Interface:<nil> Address:{IP:192.168.165.22 Mask:ffffff00} Gateway:192.168.165.2}]
		//	// Routes [{Dst:{IP:0.0.0.0 Mask:00000000} GW:<nil>}]
		//	// DNS {[]  [] []}}
		//
		//
		//
		//} else {
		//	return fmt.Errorf("IPAM???Type?????????! ???=%v", n.IPAM.Type)
		//}
	*/

	resIp := n.NetInfo.IPAddress
	resGw := n.NetInfo.GateWay
	result.IPs[0].Address.IP = []byte(resIp)
	result.IPs[0].Address.Mask = []byte("ffffff00")
	result.IPs[0].Gateway = []byte(resGw)
	log.Println("CNI ??????result??????=", result)
	return types.PrintResult(result, cniVersion)
}

func cmdCheck(args *skel.CmdArgs) error {
	log.Println("cmdCheck ??????args=", *args)
	return nil
}

func cmdDel(args *skel.CmdArgs) error {
	log.Println("cmdDel ???args=", args)
	n, _, err := loadConf(args.StdinData, args.Args)
	if err != nil {
		log.Println("cmdDel ???err=", err)
		return err
	}
	log.Println("cmdDel??????n=", n)
	podName := n.EnvArgs.K8sPodName
	podNameSpace := n.EnvArgs.K8sPodNamespace
	utils.EtcdCmdDel(podNameSpace, podName)

	if args.Netns == "" {
		return nil
	}

	// There is a netns so try to clean up. Delete can be called multiple times
	// so don't return an error if the device is already removed.
	err = ns.WithNetNSPath(args.Netns, func(_ ns.NetNS) error {
		if err := ip.DelLinkByName(args.IfName); err != nil {
			if err != ip.ErrLinkNotFound {
				return err
			}
		}
		return nil
	})

	return err
}

func main() {
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.All, bv.BuildString("tc-cni"))
}
