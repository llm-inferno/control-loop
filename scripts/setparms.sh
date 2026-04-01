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

export TUNER_HOST=localhost
export TUNER_PORT=8081

export INFERNO_CONTROL_PERIOD=60
export INFERNO_CONTROL_DYNAMIC=false

export INFERNO_LOAD_INTERVAL=20
export INFERNO_LOAD_ALPHA=0.1
export INFERNO_LOAD_THETA=0.2
export INFERNO_LOAD_SKEW=0.3

echo "==> parameters set"