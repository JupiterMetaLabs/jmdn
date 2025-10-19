package Channel


// Define the channel buffer here
// -> Pubsub module append messages to this go channel and Data Processing module will pick up the messages from 
// -  here and do the repective processing

// Make unbuffered channel -> struct as string format will be appended to this channel
var ChannelBuffer = make(chan *string)

