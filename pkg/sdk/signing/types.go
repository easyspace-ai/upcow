package signing

type SignatureFunc func(signer string, digest []byte) ([]byte, error)
