#!/bin/bash
TOPODIR="./topologies"
TOPOS=("topo-1.yml" "topo-2.yml" "topo-3.yml" "topo-4.yml")
LAST_HOUR=-1
while true; do
  HOUR=$(date -u +%H)
  INDEX=$(( 10#$HOUR % 4 ))
  TOPO=${TOPOS[$INDEX]}
  if [ "$HOUR" != "$LAST_HOUR" ]; then
    echo "Current UTC hour: $HOUR. Target TOPO: $TOPO"
    docker run --rm --privileged \
      --network host \
      --pid host \
      -v /var/run/docker.sock:/var/run/docker.sock \
      -v "$TARGET_PWD:$TARGET_PWD" \
      -w "$TARGET_PWD" \
      ghcr.io/srl-labs/clab \
      containerlab deploy -t "$TOPODIR/$TOPO" --reconfigure
    LAST_HOUR=$HOUR
  fi
  sleep 60
done
