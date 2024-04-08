# About
## Prerequirements
`/etc/docker/daemon.json` file:
```json
{
  "bridge": "none",
  "iptables": false
}
```

# Usage:
```bash
docker network create --driver bgplb --ipam-driver bgplb --subnet 200.200.202.0/24 --gateway 200.200.202.1 example
docker run -it --rm --net=example --ip 200.200.202.100 bash
```

# Architecture
Combine bridge plugin from https://github.com/KatharaFramework/NetworkPlugin together with IPAM plugin from https://github.com/ishantt/docker-ipam-plugin
Pure minimum implementation without any IP selection logic (=> user must tell IPs).

Does NOT configure default gateway for containers which trigger Docker adding second [docker_gwbridge](https://docs.docker.com/engine/swarm/networking/#customize-the-docker_gwbridge) interface as default gateway for them which why we have use this plugin to define load balancer IPs only.