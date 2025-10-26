package Sequencer

// TODO -> write go routine to desolve the pubsub channel and also start the bft process -> start bft in 20secs
type ParallelProcess interface{
	StartReceivingVotes() error // This should trigger the functions to stop receiving votes and start the bft process
	StartBFTProcess() error // This should trigger the functions to start the bft process
}
