package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	apiGoBGP "github.com/osrg/gobgp/v3/api"
	loggerGoBGP "github.com/osrg/gobgp/v3/pkg/log"
	serverGoBGP "github.com/osrg/gobgp/v3/pkg/server"
	"github.com/vishvananda/netlink"
	apb "google.golang.org/protobuf/types/known/anypb"
)

var (
	bgpServer = serverGoBGP.BgpServer{}
	localAS   = uint32(0)
	routerID  = ""
)

func startBgpServer(peerAddress string) error {
	routerID = os.Getenv("ROUTER_ID")
	if routerID == "" && net.ParseIP(routerID) == nil {
		return fmt.Errorf("Environment variable ROUTER_ID is required\r\n")
	}

	routerPortInt, err := strconv.Atoi(os.Getenv("ROUTER_PORT"))
	if err != nil {
		return fmt.Errorf("Environment variable ROUTER_PORT value is invalid\r\n")
	}
	routerPort := int32(routerPortInt)

	localAsInt, err := strconv.Atoi(os.Getenv("LOCAL_AS"))
	if err != nil {
		return fmt.Errorf("Environment variable LOCAL_AS value is invalid\r\n")
	}
	localAS = uint32(localAsInt)

	peerAsInt, err := strconv.Atoi(os.Getenv("PEER_AS"))
	if err != nil {
		return fmt.Errorf("Environment variable PEER_AS value is invalid\r\n")
	}
	peerAs := uint32(peerAsInt)

	log.Infof("Starting BGP server")
	bgpLogger := loggerGoBGP.NewDefaultLogger()
	bgpServer = *serverGoBGP.NewBgpServer(serverGoBGP.LoggerOption(bgpLogger))
	go bgpServer.Serve()
	err = bgpServer.StartBgp(context.Background(), &apiGoBGP.StartBgpRequest{
		Global: &apiGoBGP.Global{
			RouterId:   routerID,
			Asn:        localAS,
			ListenPort: routerPort,
		},
	})
	if err != nil {
		return err
	}

	n := &apiGoBGP.Peer{
		Conf: &apiGoBGP.PeerConf{
			NeighborAddress: peerAddress,
			PeerAsn:         peerAs,
		},
	}
	peerPassword := os.Getenv("PEER_PASSWORD")
	if peerPassword != "" {
		n.Conf.AuthPassword = peerPassword
	}
	if err := bgpServer.AddPeer(context.Background(), &apiGoBGP.AddPeerRequest{
		Peer: n,
	}); err != nil {
		return err
	}

	return nil
}

func addRoute(NetworkID, EndpointID, ipv4, ipv6 string) {
	if running := waitContainerHealthy(NetworkID, EndpointID); running == false {
		return
	}

	bridgeName := getBridgeNameByNetID(NetworkID)
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		log.Errorf("addRoute error: %v", err)
		return
	}
	if ipv4 != "" {
		ip, ipv4Dst, _ := net.ParseCIDR(ipv4)
		if ip.String() != "0.0.0.0" {
			log.Infof("Adding IPv4 route to %s", ipv4Dst)
			route := netlink.Route{Dst: ipv4Dst, LinkIndex: bridge.Attrs().Index}
			netlink.RouteAdd(&route)

			addBgpRoute(ip.String(), 32, apiGoBGP.Family_AFI_IP)
		}
	}
	if ipv6 != "" {
		ip, ipv6Dst, _ := net.ParseCIDR(ipv6)
		log.Infof("Adding IPv6 route to %s", ipv6Dst)
		route := netlink.Route{Dst: ipv6Dst, LinkIndex: bridge.Attrs().Index}
		netlink.RouteAdd(&route)

		addBgpRoute(ip.String(), 128, apiGoBGP.Family_AFI_IP6)
	}
}

func addBgpRoute(prefix string, mask int, ipFamily apiGoBGP.Family_Afi) error {
	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    prefix,
		PrefixLen: uint32(mask),
	})
	a1, _ := apb.New(&apiGoBGP.OriginAttribute{
		Origin: 0,
	})
	a2, _ := apb.New(&apiGoBGP.NextHopAttribute{
		NextHop: routerID,
	})
	a3, _ := apb.New(&apiGoBGP.AsPathAttribute{
		Segments: []*apiGoBGP.AsSegment{
			{
				Type: 2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	_, err := bgpServer.AddPath(context.Background(), &apiGoBGP.AddPathRequest{
		Path: &apiGoBGP.Path{
			Family: &apiGoBGP.Family{Afi: ipFamily, Safi: apiGoBGP.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	})
	return err
}

func delRoute(NetworkID, EndpointID string) {
	bridgeName := getBridgeNameByNetID(NetworkID)
	bridge, err := netlink.LinkByName(bridgeName)
	if err != nil {
		log.Errorf("delRoute error: %v", err)
		return
	}

	v4routes, err := netlink.RouteList(bridge, netlink.FAMILY_V4)
	if err != nil {
		log.Errorf("Failed to get local IPv4 routes: %v", err)
	}
	for _, v4route := range v4routes {
		v4dst := &v4route.Dst.IP
		delBgpRoute(v4dst.String(), 32, apiGoBGP.Family_AFI_IP)
		if err := netlink.RouteDel(&v4route); err != nil {
			log.Errorf("Cannot remove local route to: %v , Error: %v", v4dst.String(), err)
		}
	}
	v6routes, err := netlink.RouteList(bridge, netlink.FAMILY_V6)
	if err != nil {
		log.Errorf("Failed to get local IPv6 routes: %v", err)
	}
	for _, v6route := range v6routes {
		v6dst := &v6route.Dst.IP
		delBgpRoute(v6dst.String(), 128, apiGoBGP.Family_AFI_IP6)
		if err := netlink.RouteDel(&v6route); err != nil {
			log.Errorf("Cannot remove local route to: %v , Error: %v", v6dst.String(), err)
		}
	}
}

func delBgpRoute(prefix string, mask int, ipFamily apiGoBGP.Family_Afi) error {
	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    prefix,
		PrefixLen: uint32(mask),
	})
	a1, _ := apb.New(&apiGoBGP.OriginAttribute{
		Origin: 0,
	})
	a2, _ := apb.New(&apiGoBGP.NextHopAttribute{
		NextHop: routerID,
	})
	a3, _ := apb.New(&apiGoBGP.AsPathAttribute{
		Segments: []*apiGoBGP.AsSegment{
			{
				Type: 2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	p1 := &apiGoBGP.Path{
		Family: &apiGoBGP.Family{Afi: ipFamily, Safi: apiGoBGP.Family_SAFI_UNICAST},
		Nlri:   nlri,
		Pattrs: attrs,
	}
	err := bgpServer.DeletePath(context.Background(), &apiGoBGP.DeletePathRequest{
		TableType: apiGoBGP.TableType_GLOBAL,
		Path:      p1,
	})
	if err != nil {
		return err
	}
	return nil
}

func isPrefixAdvertised(ctx context.Context, prefix string) bool {
	_, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return false
	}

	var counter int
	callback := func(*apiGoBGP.Destination) { counter++ }
	request := &apiGoBGP.ListPathRequest{
		Family:   &apiGoBGP.Family{Afi: apiGoBGP.Family_AFI_IP, Safi: apiGoBGP.Family_SAFI_UNICAST},
		Prefixes: []*apiGoBGP.TableLookupPrefix{{Prefix: prefix}},
	}

	if ipnet.IP.To4() == nil && strings.Contains(ipnet.IP.String(), ":") {
		request.Family.Afi = apiGoBGP.Family_AFI_IP6
	}

	bgpServer.ListPath(ctx, request, callback)

	return counter > 0
}

func advertisePrefix(ctx context.Context, prefix string) error {
	_, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("advertisePrefix: failed to parse the prefix: %w", err)
	}

	mask, _ := ipnet.Mask.Size()
	var family apiGoBGP.Family_Afi
	if ipnet.IP.To4() == nil && strings.Contains(ipnet.IP.String(), ":") {
		family = apiGoBGP.Family_AFI_IP6
	} else {
		family = apiGoBGP.Family_AFI_IP
	}

	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    ipnet.IP.String(),
		PrefixLen: uint32(mask),
	})
	a1, _ := apb.New(&apiGoBGP.OriginAttribute{
		Origin: 0,
	})
	a2, _ := apb.New(&apiGoBGP.NextHopAttribute{
		NextHop: routerID,
	})
	a3, _ := apb.New(&apiGoBGP.AsPathAttribute{
		Segments: []*apiGoBGP.AsSegment{
			{
				Type: 2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	if _, err := bgpServer.AddPath(ctx, &apiGoBGP.AddPathRequest{
		Path: &apiGoBGP.Path{
			Family: &apiGoBGP.Family{Afi: family, Safi: apiGoBGP.Family_SAFI_UNICAST},
			Nlri:   nlri,
			Pattrs: attrs,
		},
	}); err != nil {
		return fmt.Errorf("advertisePrefix: failed to add the prefix: %w", err)
	}

	return nil
}

func withdrawPrefix(ctx context.Context, prefix string) error {
	_, ipnet, err := net.ParseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("withdrawPrefix: failed to parse the prefix: %w", err)
	}

	mask, _ := ipnet.Mask.Size()
	var family apiGoBGP.Family_Afi
	if ipnet.IP.To4() == nil && strings.Contains(ipnet.IP.String(), ":") {
		family = apiGoBGP.Family_AFI_IP6
	} else {
		family = apiGoBGP.Family_AFI_IP
	}

	nlri, _ := apb.New(&apiGoBGP.IPAddressPrefix{
		Prefix:    ipnet.IP.String(),
		PrefixLen: uint32(mask),
	})
	a1, _ := apb.New(&apiGoBGP.OriginAttribute{
		Origin: 0,
	})
	a2, _ := apb.New(&apiGoBGP.NextHopAttribute{
		NextHop: routerID,
	})
	a3, _ := apb.New(&apiGoBGP.AsPathAttribute{
		Segments: []*apiGoBGP.AsSegment{
			{
				Type: 2,
			},
		},
	})
	attrs := []*apb.Any{a1, a2, a3}
	p1 := &apiGoBGP.Path{
		Family: &apiGoBGP.Family{Afi: family, Safi: apiGoBGP.Family_SAFI_UNICAST},
		Nlri:   nlri,
		Pattrs: attrs,
	}
	if err := bgpServer.DeletePath(ctx, &apiGoBGP.DeletePathRequest{
		TableType: apiGoBGP.TableType_GLOBAL,
		Path:      p1,
	}); err != nil {
		return fmt.Errorf("withdrawPrefix: failed to delete the prefix: %w", err)
	}

	return nil
}
