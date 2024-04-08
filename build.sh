#!/bin/bash

USAGE="Usage: ./build.sh <Docker Hub Organization> <version>"

if [ "$1" == "--help" ] || [ "$#" -lt "2" ]; then
	echo $USAGE
	exit 0
fi

ORG=$1
VERSION=$2

rm -rf rootfs
docker plugin disable $ORG/docker-bgp-lb:v$VERSION
docker plugin rm $ORG/docker-bgp-lb:v$VERSION

mkdir -p rootfs
CGO_ENABLED=0 go build -a -tags netgo -ldflags '-w -extldflags "-static"'
cp docker-bgp-lb rootfs/

docker plugin create $ORG/docker-bgp-lb:v$VERSION .
docker plugin enable $ORG/docker-bgp-lb:v$VERSION
docker plugin push $ORG/docker-bgp-lb:v$VERSION
