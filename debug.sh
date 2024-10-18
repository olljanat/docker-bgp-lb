#!/bin/bash

go build

export ROUTER_ID=192.168.8.40
export ROUTER_PORT=-1
export LOCAL_AS=64512
export PEER_ADDRESS=192.168.8.137
export PEER_AS=65500
export SIGUSR2_HANDLER=true
sudo -E ./docker-bgp-lb

# Command to create test container
: '
docker network create \
  --driver bgplb \
  --ipam-driver bgplb \
  --subnet 10.0.0.101/32 \
   web1
'
