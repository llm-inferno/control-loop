#!/bin/bash
. ./setenv.sh
echo "Launching terminals ..."

# load
term -p 50,50 -s 400,300 $CMD_DIR/loademulator set_theme black orange
sleep 1

# collector
term -p 50,200 -s 800,300 $CMD_DIR/collector set_theme black green1
sleep 1

# optimizer
term -p 50,550 -s 800,300 $CMD_DIR/optimizer set_theme black red
sleep 1

# actuator
term -p 50,900 -s 800,300 $CMD_DIR/actuator set_theme aquamarine blue
sleep 1

# controller
term -p 1200,50 -s 800,300 $CMD_DIR/controller set_theme black yellow
sleep 1

# watch
term -p 1200,500 -s 800,400 set_theme red beige
sleep 1

# launcher
term -p 1500,850 -s 500,300 $YAMLS_DIR/workload set_theme yellow DarkGreen
sleep 1
