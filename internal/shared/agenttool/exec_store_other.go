//go:build !windows && !darwin

package agenttool

func userTrustStore() execTrustStore { return nil }
