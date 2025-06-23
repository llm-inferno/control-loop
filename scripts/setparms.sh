#!/bin/bash
############################################################
# Parameters
############################################################

# set if external cluster mode
export KUBECONFIG=$HOME/.kube/config

export CONTROLLER_HOST=localhost
export CONTROLLER_PORT=3300

export COLLECTOR_HOST=localhost
export COLLECTOR_PORT=3301

export INFERNO_HOST=localhost
export INFERNO_PORT=3302

export ACTUATOR_HOST=localhost
export ACTUATOR_PORT=3303

export INFERNO_CONTROL_PERIOD=60
export INFERNO_CONTROL_DYNAMIC=false

echo "==> parameters set"