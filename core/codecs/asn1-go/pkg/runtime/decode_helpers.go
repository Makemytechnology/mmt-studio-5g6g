// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import "fmt"

// APERDecodable is implemented by every generated message type. The compiler
// emits both `MarshalAPER`/`UnmarshalAPER` and (when the encoding mode is
// "both" or "uper") `MarshalUPER`/`UnmarshalUPER`, so any of them satisfies
// the relevant interface.
type APERDecodable interface {
	UnmarshalAPER([]byte) error
}

type UPERDecodable interface {
	UnmarshalUPER([]byte) error
}

// DecodeAPERToJSON decodes APER bytes into the supplied (typed) receiver and
// returns a pretty-printed JSON view. The receiver must be a pointer to a
// generated message type.
//
// Example:
//
//	var msg ngap.NGSetupRequest
//	out, err := runtime.DecodeAPERToJSON(rawBytes, &msg)
//	if err != nil { ... }
//	fmt.Println(out)
func DecodeAPERToJSON(b []byte, receiver APERDecodable) (string, error) {
	if err := receiver.UnmarshalAPER(b); err != nil {
		return "", fmt.Errorf("decode APER: %w", err)
	}
	return MustPrettyJSON(receiver), nil
}

// DecodeUPERToJSON is the UPER counterpart to DecodeAPERToJSON.
func DecodeUPERToJSON(b []byte, receiver UPERDecodable) (string, error) {
	if err := receiver.UnmarshalUPER(b); err != nil {
		return "", fmt.Errorf("decode UPER: %w", err)
	}
	return MustPrettyJSON(receiver), nil
}
