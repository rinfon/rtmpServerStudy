package ts

import (
	"fmt"
	"rtmpServerStudy/av"
	"rtmpServerStudy/aacParse"
	"rtmpServerStudy/h264Parse"
	"rtmpServerStudy/ts/tsio"
	"io"
	"time"
	//"encoding/hex"
)

//now just support aac h264 for ts
var CodecTypes = []av.CodecType{av.H264, av.AAC}

type Muxer struct {
	w                        io.Writer
	//streams                  []*Stream
	astream *Stream
	vstream *Stream
	PaddingToMakeCounterCont bool

	psidata []byte
	peshdr  []byte
	tshdr   []byte
	adtshdr []byte
	datav   [][]byte
	nalus   [][]byte

	tswpat, tswpmt *tsio.TSWriter
}


func NewMuxer(w io.Writer) *Muxer {
	return &Muxer{
		w:       w,
		psidata: make([]byte, 188),
		peshdr:  make([]byte, tsio.MaxPESHeaderLength),
		tshdr:   make([]byte, tsio.MaxTSHeaderLength),
		adtshdr: make([]byte, aacparser.ADTSHeaderLength),
		nalus:   make([][]byte, 16),
		datav:   make([][]byte, 16),
		tswpmt:  tsio.NewTSWriter(tsio.PMT_PID),
		tswpat:  tsio.NewTSWriter(tsio.PAT_PID),
	}
}


const	(
	videoPid = 0x100
	audioPid = 0x101
)
//new stream
func (self *Muxer) newStream(codec av.CodecData) (err error) {
	ok := false
	for _, c := range CodecTypes {
		if codec.Type() == c {
			ok = true
			break
		}
	}
	if !ok {
		err = fmt.Errorf("ts: codec type=%s is not supported", codec.Type())
		return
	}
	switch codec.Type() {
	case av.H264:
		pid:=videoPid
		stream := &Stream{
			muxer:     self,
			CodecData: codec,
			pid:       pid,
			tsw:       tsio.NewTSWriter(pid),
		}
		self.vstream = stream
	case av.NELLYMOSER:
	case av.SPEEX:
	case av.AAC:
		pid:=audioPid
		stream := &Stream{
			muxer:     self,
			CodecData: codec,
			pid:       pid,
			tsw:       tsio.NewTSWriter(pid),
		}
		self.astream = stream
	default:
		err = fmt.Errorf("ts.Unspported.CodecType(%v)", codec.Type())
		return
	}
	return
}

func (self *Muxer) writePaddingTSPackets(tsw *tsio.TSWriter) (err error) {
	for tsw.ContinuityCounter&0xf != 0x0 {
		if err = tsw.WritePackets(self.w, self.datav[:0], 0, false, true); err != nil {
			return
		}
	}
	return
}

func (self *Muxer) WriteTrailer() (err error) {

	if self.PaddingToMakeCounterCont {
		if self.astream != nil {
			if err = self.writePaddingTSPackets(self.astream.tsw); err != nil {
				return
			}
		}
		if self.vstream != nil {
			if err = self.writePaddingTSPackets(self.vstream.tsw); err != nil {
				return
			}
		}
	}

	return
}

func (self *Muxer) SetWriter(w io.Writer) {
	self.w = w
	return
}

func (self *Muxer) WritePATPMT() (err error) {
	pat := tsio.PAT{
		Entries: []tsio.PATEntry{
			{ProgramNumber: 1, ProgramMapPID: tsio.PMT_PID},
		},
	}
	patlen := pat.Marshal(self.psidata[tsio.PSIHeaderLength:])
	n := tsio.FillPSI(self.psidata, tsio.TableIdPAT, tsio.TableExtPAT, patlen)
	self.datav[0] = self.psidata[:n]
	if err = self.tswpat.WritePackets(self.w, self.datav[:1], 0, false, true); err != nil {
		return
	}

	var elemStreams []tsio.ElementaryStreamInfo

	if self.astream != nil {
		switch self.astream.Type() {
		case av.AAC:
			elemStreams = append(elemStreams, tsio.ElementaryStreamInfo{
				StreamType:    tsio.ElementaryStreamTypeAdtsAAC,
				ElementaryPID: self.astream.pid,
			})
		}
	}else{
		// for debug log
	}

	if self.vstream != nil {
		switch self.vstream.Type() {
		case av.H264:
			elemStreams = append(elemStreams, tsio.ElementaryStreamInfo{
				StreamType:    tsio.ElementaryStreamTypeH264,
				ElementaryPID: self.vstream.pid,
			})
		}
	}else{
		//for debug log
	}

	pmt := tsio.PMT{
		PCRPID:                0x100,
		ElementaryStreamInfos: elemStreams,
	}

	pmtlen := pmt.Len()
	if pmtlen+tsio.PSIHeaderLength > len(self.psidata) {
		err = fmt.Errorf("ts.PMT.Too.Large")
		return
	}

	pmt.Marshal(self.psidata[tsio.PSIHeaderLength:])
	n = tsio.FillPSI(self.psidata, tsio.TableIdPMT, tsio.TableExtPMT, pmtlen)
	self.datav[0] = self.psidata[:n]
	if err = self.tswpmt.WritePackets(self.w, self.datav[:1], 0, false, true); err != nil {
		return
	}

	return
}

func (self *Muxer) WriteHeader(astream av.CodecData,vstream av.CodecData) (err error) {

	//audio stream
	if astream != nil {
		if err = self.newStream(astream); err != nil {
			return
		}
	}

	//vedio stream
	if vstream != nil {
		if err = self.newStream(vstream); err != nil {
			return
		}
	}

	//write pat pmt
	if err = self.WritePATPMT(); err != nil {
		return
	}
	return
}

func (self *Muxer) WritePacket(pkt av.Packet,Cstream av.CodecData) (err error) {

	pkt.Time += time.Second
	switch Cstream.Type() {
	case av.AAC:

		codec := self.astream.CodecData.(aacparser.CodecData)
		n := tsio.FillPESHeader(self.peshdr, tsio.StreamIdAAC, len(self.adtshdr)+len(pkt.Data), pkt.Time, 0)
		self.datav[0] = self.peshdr[:n]
		aacparser.FillADTSHeader(self.adtshdr, codec.Config, 1024, len(pkt.Data))
		self.datav[1] = self.adtshdr
		self.datav[2] = pkt.Data

		if err = self.astream.tsw.WritePackets(self.w, self.datav[:3], pkt.Time, true, false); err != nil {
			return
		}

	case av.H264:
		codec := self.vstream.CodecData.(h264parser.CodecData)

		nalus := self.nalus[:0]
		if pkt.IsKeyFrame {
			nalus = append(nalus, codec.SPS())
			nalus = append(nalus, codec.PPS())
		}

		pktnalus, _ := h264parser.SplitNALUs(pkt.Data)
		for _, pktnalu:= range pktnalus {
			nalus = append(nalus, pktnalu)
		}

		datav := self.datav[:1]
		for i,nal:= range nalus {
			if i == 0 {
				datav = append(datav, h264parser.AUDBytes)
			} else {
				datav = append(datav, h264parser.StartCodeBytes)
			}
			datav = append(datav, nal)
		}

		n := tsio.FillPESHeader(self.peshdr, tsio.StreamIdH264, -1, pkt.Time+pkt.CompositionTime, pkt.Time)
		datav[0] = self.peshdr[:n]

		if err = self.vstream.tsw.WritePackets(self.w, datav, pkt.Time, pkt.IsKeyFrame, false); err != nil {
			return
		}
	}
	return
}