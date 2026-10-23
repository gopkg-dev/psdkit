package psdedit

import "github.com/gopkg-dev/psdkit/psdfile"

// effectiveSourceChannel maps immutable mask channel records after vector removal without decoding their samples.
func effectiveSourceChannel(node *editNode, channel psdfile.ChannelRecord) (psdfile.ChannelRecord, bool) {
	if change := node.vectorMaskChange; change != nil {
		if channel.ID == -2 && change.dropGenerated {
			return psdfile.ChannelRecord{}, false
		}
		if channel.ID == -3 && change.promoteReal {
			channel.ID = -2
		}
	}
	return channel, true
}
