package sshsign

import (
	"crypto/rand"
	"errors"

	"golang.org/x/crypto/ssh"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	flagRSASHA256 = 2
	flagRSASHA512 = 4
)

type Keys struct{}

var _ session.SSHKeys = Keys{}

var errKey = errors.New("sshsign: not a private key this engine signs with")

func signer(privateKey string) (ssh.Signer, error) {
	raw, err := ssh.ParseRawPrivateKey([]byte(privateKey))
	if err != nil {
		return nil, errKey
	}
	s, err := ssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, errKey
	}
	return s, nil
}

func (Keys) PublicKey(privateKey string) ([]byte, error) {
	s, err := signer(privateKey)
	if err != nil {
		return nil, err
	}
	return s.PublicKey().Marshal(), nil
}

func (Keys) Sign(privateKey string, data []byte, flags uint32) (session.SSHSignature, error) {
	s, err := signer(privateKey)
	if err != nil {
		return session.SSHSignature{}, err
	}
	var sig *ssh.Signature
	if as, ok := s.(ssh.AlgorithmSigner); ok && s.PublicKey().Type() == ssh.KeyAlgoRSA {
		algo := ssh.KeyAlgoRSA
		switch {
		case flags&flagRSASHA512 != 0:
			algo = ssh.KeyAlgoRSASHA512
		case flags&flagRSASHA256 != 0:
			algo = ssh.KeyAlgoRSASHA256
		}
		sig, err = as.SignWithAlgorithm(rand.Reader, data, algo)
	} else {
		sig, err = s.Sign(rand.Reader, data)
	}
	if err != nil {
		return session.SSHSignature{}, errors.New("sshsign: the key didn't sign")
	}
	return session.SSHSignature{Format: sig.Format, Blob: sig.Blob, Rest: sig.Rest}, nil
}
