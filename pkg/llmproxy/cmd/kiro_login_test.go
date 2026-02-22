package cmd

import (
	"errors"
	"testing"
)

func TestIsKiroAWSAccessPortalError(t *testing.T) {
	if !isKiroAWSAccessPortalError(errors.New("AWS access portal sign in error: retry later")) {
		t.Fatal("expected access portal error to be detected")
	}
	if !isKiroAWSAccessPortalError(errors.New("We were unable to sign you in to the AWS access portal.")) {
		t.Fatal("expected access portal phrase to be detected")
	}
	if isKiroAWSAccessPortalError(errors.New("network timeout")) {
		t.Fatal("did not expect unrelated error to be detected")
	}
}
