package aac

/*
#include <stdlib.h>
#include "voAAC.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"unsafe"
)

// Standard error codes returned by the VisualOn AAC encoder.
var (
	ErrFinish            = errors.New("error finish")
	ErrFailed            = errors.New("process data failed")
	ErrOutOfMemory       = errors.New("out of memory")
	ErrNotImplement      = errors.New("feature not implemented")
	ErrInvalidArg        = errors.New("invalid argument")
	ErrInputBufferSmall  = errors.New("input buffer data too small")
	ErrOutputBufferSmall = errors.New("output buffer size too small")
	ErrWrongStatus       = errors.New("wrong encoder run-time status")
	ErrWrongParamID      = errors.New("wrong parameter id")
)

func errorFromResult(r C.VO_U32) error {
	switch r {
	case C.VO_ERR_NONE:
		return nil
	case C.VO_ERR_FINISH:
		return ErrFinish
	case C.VO_ERR_FAILED:
		return ErrFailed
	case C.VO_ERR_OUTOF_MEMORY:
		return ErrOutOfMemory
	case C.VO_ERR_NOT_IMPLEMENT:
		return ErrNotImplement
	case C.VO_ERR_INVALID_ARG:
		return ErrInvalidArg
	case C.VO_ERR_INPUT_BUFFER_SMALL:
		return ErrInputBufferSmall
	case C.VO_ERR_OUTPUT_BUFFER_SMALL:
		return ErrOutputBufferSmall
	case C.VO_ERR_WRONG_STATUS:
		return ErrWrongStatus
	case C.VO_ERR_WRONG_PARAM_ID:
		return ErrWrongParamID
	default:
		return fmt.Errorf("aac encoder error: 0x%x", uint32(r))
	}
}

// Options represents AAC encoding configuration options.
type Options struct {
	SampleRate  int
	BitRate     int
	NumChannels int
}

// Encoder represents an instance-isolated, thread-safe AAC encoder.
// Each instance holds its own C handle, preventing any cross-talk or race conditions
// when multiple cameras transcode audio simultaneously.
type Encoder struct {
	mu     sync.Mutex
	handle C.VO_HANDLE
	w      io.Writer
	insize int
	inbuf  []byte
	outbuf []byte
	closed bool
}

// NewEncoder creates a new instance-isolated AAC encoder.
func NewEncoder(w io.Writer, opts *Options) (*Encoder, error) {
	if opts.BitRate == 0 {
		opts.BitRate = 64000
	}

	var handle C.VO_HANDLE
	ret := C.voAACEncInit(&handle, C.VO_AUDIO_CodingAAC, nil)
	if err := errorFromResult(ret); err != nil {
		return nil, fmt.Errorf("voAACEncInit failed: %w", err)
	}

	var params C.AACENC_PARAM
	params.sampleRate = C.int(opts.SampleRate)
	params.bitRate = C.int(opts.BitRate)
	params.nChannels = C.short(opts.NumChannels)
	params.adtsUsed = 1 // Generate ADTS framing

	ret = C.voAACEncSetParam(handle, C.VO_PID_AAC_ENCPARAM, C.VO_PTR(&params))
	if err := errorFromResult(ret); err != nil {
		_ = C.voAACEncUninit(handle)
		return nil, fmt.Errorf("voAACEncSetParam failed: %w", err)
	}

	insize := opts.NumChannels * 2 * 1024

	return &Encoder{
		handle: handle,
		w:      w,
		insize: insize,
		inbuf:  make([]byte, insize),
		outbuf: make([]byte, 20480),
	}, nil
}

// Encode encodes raw PCM audio from r into the writer configured in NewEncoder.
func (e *Encoder) Encode(r io.Reader) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed || e.handle == nil {
		return errors.New("encoder is closed")
	}

	inputEmpty := false
	for !inputEmpty {
		n, err := r.Read(e.inbuf)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return fmt.Errorf("aac read error: %w", err)
			}
			inputEmpty = true
		}

		if n < e.insize {
			inputEmpty = true
		}
		if n == 0 {
			break
		}

		var outinfo C.VO_AUDIO_OUTPUTINFO
		var input C.VO_CODECBUFFER

		cInbuf := C.CBytes(e.inbuf[:n])
		input.Buffer = (*C.VO_U8)(cInbuf)
		input.Length = C.VO_U32(n)

		ret := C.voAACEncSetInputData(e.handle, &input)
		err = errorFromResult(ret)
		if err != nil {
			C.free(cInbuf)
			return fmt.Errorf("voAACEncSetInputData failed: %w", err)
		}

		outputEmpty := false
		for !outputEmpty {
			var output C.VO_CODECBUFFER
			cOutbuf := C.CBytes(e.outbuf)
			output.Buffer = (*C.VO_U8)(cOutbuf)
			output.Length = C.VO_U32(len(e.outbuf))

			ret = C.voAACEncGetOutputData(e.handle, &output, &outinfo)
			err = errorFromResult(ret)
			if err != nil {
				if !errors.Is(err, ErrInputBufferSmall) {
					C.free(cOutbuf)
					C.free(cInbuf)
					return fmt.Errorf("voAACEncGetOutputData failed: %w", err)
				}
				outputEmpty = true
			}

			if output.Length > 0 {
				_, writeErr := e.w.Write(C.GoBytes(unsafe.Pointer(output.Buffer), C.int(output.Length)))
				if writeErr != nil {
					C.free(cOutbuf)
					C.free(cInbuf)
					return fmt.Errorf("aac write error: %w", writeErr)
				}
			}
			C.free(cOutbuf)
		}

		C.free(cInbuf)
	}

	return nil
}

// Close releases the encoder instance and frees native C resources.
func (e *Encoder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed || e.handle == nil {
		return nil
	}

	ret := C.voAACEncUninit(e.handle)
	e.handle = nil
	e.closed = true

	return errorFromResult(ret)
}
