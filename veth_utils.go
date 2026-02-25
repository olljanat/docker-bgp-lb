package main

import (
	"strings"

	"github.com/google/uuid"
	"github.com/vishvananda/netlink"
)

const (
	vethNameLen    = 8
	vethNamePrefix = "veth"
)

func getVethRandomName() string {
	randomUuid, _ := uuid.NewRandom()

	return vethNamePrefix + strings.Replace(randomUuid.String(), "-", "", -1)[:vethNameLen]
}

func createVethPair() (string, string, error) {
	vethName1 := getVethRandomName()
	vethName2 := getVethRandomName()

	linkAttrs := netlink.NewLinkAttrs()
	linkAttrs.Name = vethName1

	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: linkAttrs,
		PeerName:  vethName2,
	}); err != nil {
		return "", "", err
	}

	return vethName1, vethName2, nil
}

func deleteVethPair(vethOutside string) error {
	iface, err := netlink.LinkByName(vethOutside)
	if err != nil {
		return err
	}

	if err := netlink.LinkDel(iface); err != nil {
		return err
	}

	return nil
}
