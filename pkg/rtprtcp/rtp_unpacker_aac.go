// Copyright 2020, Chef.  All rights reserved.
// https://github.com/q191201771/lal
//
// Use of this source code is governed by a MIT-style license
// that can be found in the License file.
//
// Author: Chef (191201771@qq.com)

package rtprtcp

import (
	"github.com/q191201771/lal/pkg/base"
	"github.com/q191201771/naza/pkg/nazalog"
)

type RTPUnpackerAAC struct {
	payloadType base.AVPacketPT
	clockRate   int
	onAVPacket  OnAVPacket
}

func NewRTPUnpackerAAC(payloadType base.AVPacketPT, clockRate int, onAVPacket OnAVPacket) *RTPUnpackerAAC {
	return &RTPUnpackerAAC{
		payloadType: payloadType,
		clockRate:   clockRate,
		onAVPacket:  onAVPacket,
	}
}

func (unpacker *RTPUnpackerAAC) CalcPositionIfNeeded(pkt *RTPPacket) {
	// noop
}

func (unpacker *RTPUnpackerAAC) TryUnpackOne(list *RTPPacketList) (unpackedFlag bool, unpackedSeq uint16) {
	// rfc3640 2.11.  Global Structure of Payload Format
	//
	// +---------+-----------+-----------+---------------+
	// | RTP     | AU Header | Auxiliary | Access Unit   |
	// | Header  | Section   | Section   | Data Section  |
	// +---------+-----------+-----------+---------------+
	//
	//           <----------RTP Packet Payload----------->
	//
	// rfc3640 3.2.1.  The AU Header Section
	//
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+- .. -+-+-+-+-+-+-+-+-+-+
	// |AU-headers-length|AU-header|AU-header|      |AU-header|padding|
	// |                 |   (1)   |   (2)   |      |   (n)   | bits  |
	// +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+- .. -+-+-+-+-+-+-+-+-+-+
	//
	// rfc3640 3.3.6.  High Bit-rate AAC
	//
	// rtp_parse_mp4_au()
	//
	//
	// 3.2.3.1.  Fragmentation
	//
	//   A packet SHALL carry either one or more complete Access Units, or a
	//   single fragment of an Access Unit.  Fragments of the same Access Unit
	//   have the same time stamp but different RTP sequence numbers.  The
	//   marker bit in the RTP header is 1 on the last fragment of an Access
	//   Unit, and 0 on all other fragments.
	//

	p := list.head.next // first
	if p == nil {
		return false, 0
	}
	b := p.packet.Raw[p.packet.Header.payloadOffset:]
	//nazalog.Debugf("%d, %d, %s", len(pkt.Raw), pkt.Header.timestamp, hex.Dump(b))

	aus := parseAU(b)

	if len(aus) == 1 {
		if aus[0].size <= uint32(len(b[aus[0].pos:])) {
			// one complete access unit
			var outPkt base.AVPacket
			outPkt.PayloadType = unpacker.payloadType
			outPkt.Timestamp = p.packet.Header.Timestamp / uint32(unpacker.clockRate/1000)
			outPkt.Payload = b[aus[0].pos : aus[0].pos+aus[0].size]
			unpacker.onAVPacket(outPkt)

			list.head.next = p.next
			list.size--
			return true, p.packet.Header.Seq
		}

		// fragmented
		// 注意，这里我们参考size和rtp包头中的timestamp，不参考rtp包头中的mark位

		totalSize := aus[0].size
		timestamp := p.packet.Header.Timestamp

		var as [][]byte
		as = append(as, b[aus[0].pos:])
		cacheSize := uint32(len(b[aus[0].pos:]))

		seq := p.packet.Header.Seq
		p = p.next
		packetCount := 0
		for {
			packetCount++
			if p == nil {
				return false, 0
			}
			if SubSeq(p.packet.Header.Seq, seq) != 1 {
				return false, 0
			}
			if p.packet.Header.Timestamp != timestamp {
				nazalog.Errorf("fragments of the same access shall have the same timestamp. first=%d, curr=%d",
					timestamp, p.packet.Header.Timestamp)
				return false, 0
			}

			b = p.packet.Raw[p.packet.Header.payloadOffset:]
			aus := parseAU(b)
			if len(aus) != 1 {
				nazalog.Errorf("shall be a single fragment. len(aus)=%d", len(aus))
				return false, 0
			}
			if aus[0].size != totalSize {
				nazalog.Errorf("fragments of the same access shall have the same size. first=%d, curr=%d",
					totalSize, aus[0].size)
				return false, 0
			}

			cacheSize += uint32(len(b[aus[0].pos:]))
			seq = p.packet.Header.Seq
			as = append(as, b[aus[0].pos:])
			if cacheSize < totalSize {
				p = p.next
			} else if cacheSize == totalSize {
				var outPkt base.AVPacket
				outPkt.PayloadType = unpacker.payloadType
				outPkt.Timestamp = p.packet.Header.Timestamp / uint32(unpacker.clockRate/1000)
				for _, a := range as {
					outPkt.Payload = append(outPkt.Payload, a...)
				}
				unpacker.onAVPacket(outPkt)

				list.head.next = p.next
				list.size -= packetCount
				return true, p.packet.Header.Seq
			} else {
				nazalog.Errorf("cache size bigger then total size. cacheSize=%d, totalSize=%d",
					cacheSize, totalSize)
				return false, 0
			}
		}
		// can reach here
	}

	// more complete access unit
	for i := range aus {
		var outPkt base.AVPacket
		outPkt.PayloadType = unpacker.payloadType
		outPkt.Timestamp = p.packet.Header.Timestamp / uint32(unpacker.clockRate/1000)
		// TODO chef: 这里1024的含义
		outPkt.Timestamp += uint32(i * (1024 * 1000) / unpacker.clockRate)
		outPkt.Payload = b[aus[i].pos : aus[i].pos+aus[i].size]
		unpacker.onAVPacket(outPkt)
	}

	list.head.next = p.next
	list.size--
	return true, p.packet.Header.Seq
}

type au struct {
	size uint32
	pos  uint32
}

func parseAU(b []byte) (ret []au) {
	// AU Header Section
	var auHeadersLength uint32
	auHeadersLength = uint32(b[0])<<8 + uint32(b[1])
	auHeadersLength = (auHeadersLength + 7) / 8

	// TODO chef: 这里的2是写死的，正常是外部传入auSize和auIndex所占位数的和
	const auHeaderSize = 2
	nbAUHeaders := uint32(auHeadersLength) / auHeaderSize // 有多少个AU-Header

	pauh := uint32(2)                  // AU Header pos
	pau := uint32(2) + auHeadersLength // AU pos

	for i := uint32(0); i < nbAUHeaders; i++ {
		// TODO chef: auSize和auIndex所在的位数是写死的13bit，3bit，标准的做法应该从外部传入，比如从sdp中获取后传入
		auSize := uint32(b[pauh])<<8 | uint32(b[pauh+1]&0xF8) // 13bit
		auSize /= 8
		// 注意，fragment时，auIndex并不可靠。见TestAACCase1
		//auIndex := b[pauh+1] & 0x7
		//nazalog.Debugf("~ %d %d", auSize, auIndex)

		ret = append(ret, au{
			size: auSize,
			pos:  pau,
		})

		pauh += 2
		pau += auSize
	}

	if (nbAUHeaders > 1 && pau != uint32(len(b))) ||
		(nbAUHeaders == 1 && pau < uint32(len(b))) {
		nazalog.Warnf("rtp packet size invalid. nbAUHeaders=%d, pau=%d, len(b)=%d", nbAUHeaders, pau, len(b))
	}

	return
}
