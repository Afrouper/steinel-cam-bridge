package aac

import (
	"bytes"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSingleEncoder(t *testing.T) {
	buf := &bytes.Buffer{}
	enc, err := NewEncoder(buf, &Options{
		SampleRate:  16000,
		BitRate:     64000,
		NumChannels: 1,
	})
	require.NoError(t, err)
	defer func() { _ = enc.Close() }()

	// 1024 samples of 16-bit PCM = 2048 bytes
	pcm := make([]byte, 2048)
	err = enc.Encode(bytes.NewReader(pcm))
	require.NoError(t, err)
	assert.Greater(t, buf.Len(), 0)
}

func TestConcurrentMultipleEncoders(t *testing.T) {
	const numEncoders = 5
	const iterations = 10

	var wg sync.WaitGroup
	wg.Add(numEncoders)

	for i := 0; i < numEncoders; i++ {
		go func(id int) {
			defer wg.Done()

			buf := &bytes.Buffer{}
			enc, err := NewEncoder(buf, &Options{
				SampleRate:  16000,
				BitRate:     64000,
				NumChannels: 1,
			})
			if err != nil {
				t.Errorf("Encoder %d failed to create: %v", id, err)
				return
			}
			defer func() { _ = enc.Close() }()

			pcm := make([]byte, 2048)
			for j := 0; j < iterations; j++ {
				err := enc.Encode(bytes.NewReader(pcm))
				if err != nil {
					t.Errorf("Encoder %d failed iteration %d: %v", id, j, err)
					return
				}
			}

			if buf.Len() == 0 {
				t.Errorf("Encoder %d produced 0 bytes", id)
			}
		}(i)
	}

	wg.Wait()
}

func TestCloseOneDoesNotAffectOthers(t *testing.T) {
	buf1 := &bytes.Buffer{}
	buf2 := &bytes.Buffer{}

	enc1, err := NewEncoder(buf1, &Options{SampleRate: 16000, BitRate: 64000, NumChannels: 1})
	require.NoError(t, err)

	enc2, err := NewEncoder(buf2, &Options{SampleRate: 16000, BitRate: 64000, NumChannels: 1})
	require.NoError(t, err)
	defer func() { _ = enc2.Close() }()

	pcm := make([]byte, 2048)

	// Close enc1
	err = enc1.Close()
	require.NoError(t, err)

	// enc2 must continue working without SIGSEGV or errors
	err = enc2.Encode(bytes.NewReader(pcm))
	require.NoError(t, err)
	assert.Greater(t, buf2.Len(), 0)
}
