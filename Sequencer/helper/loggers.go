package helper

import (
	"gossipnode/AVC/BuddyNodes/MessagePassing"
	"gossipnode/config"
)

/*
This function initializes the loggers for the Sequencer.
What it does:
if LOKI_URL is not empty, it initializes the loggers for the MessagePassing package
if LOKI_URL is empty, it initializes the loggers for the MessagePassing package without Loki

*/
func INITLoggers() {
	MessagePassing.Init_Loggers(config.LOKI_URL != "")
}