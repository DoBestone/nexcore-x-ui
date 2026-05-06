package service

import (
	"crypto/x509"
)

func parseLeaf(der []byte) (*x509.Certificate, error) {
	return x509.ParseCertificate(der)
}
