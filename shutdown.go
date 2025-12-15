package main


import (
	"gossipnode/config/GRO"
)

func shutdown(){
	GRO.GlobalGRO.Shutdown(true)
}