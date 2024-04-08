package main

import (
	"context"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

func waitContainerHealthy(networkID, endpointID string) {
	time.Sleep(1 * time.Second)
	for {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			log.Errorf("Cannot connect to Docker: %v", err)
		}

		network, err := cli.NetworkInspect(context.Background(), networkID, types.NetworkInspectOptions{})
		if err != nil {
			log.Errorf("Cannot inspect network: %v", err)
		}

		for containerID, endpoint := range network.Containers {
			if endpoint.EndpointID == endpointID {
				container, _ := cli.ContainerInspect(context.Background(), containerID)
				if container.State != nil {
					if container.State.Health != nil {
						if container.State.Health.Status == "healthy" {
							log.Infof("Container %s healthy", container.Name )
							return
						}
					} else {
						if container.State.Running == true {
							log.Infof("Container %s running", container.Name )
							return
						}
					}
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
}
