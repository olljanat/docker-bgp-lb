# About
## Prerequirements
`/etc/docker/daemon.json` file:
```json
{
  "bridge": "none",
  "iptables": false
}
```

# Installation
```bash
docker plugin install \
  --grant-all-permissions \
  ollijanatuinen/docker-bgp-lb:v0.2 \
  ROUTER_ID=<local IP> \
  LOCAL_AS=65534 \
  PEER_ADDRESS=<peer IP> \
  PEER_AS=65533
```

# Usage
```bash
docker network create \
  --driver ollijanatuinen/docker-bgp-lb:v0.2 \
  --ipam-driver ollijanatuinen/docker-bgp-lb:v0.2 \
  --subnet 200.200.200.200/32 \
  example

docker run -it --rm \
  --net=example \
  bash
```

# Architecture
Combine bridge plugin from [Kathará](https://github.com/KatharaFramework/NetworkPlugin) together with [Sample IPAM plugin](https://github.com/ishantt/docker-ipam-plugin) and [GoBGP](https://github.com/osrg/gobgp/). Pure minimum implementation without any IP selection logic (=> user must tell IPs).

Does NOT configure default gateway for containers which trigger Docker adding second [docker_gwbridge](https://docs.docker.com/engine/swarm/networking/#customize-the-docker_gwbridge) interface as default gateway for them which why we have use this plugin to define load balancer IPs only.
