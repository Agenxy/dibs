// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

package hostname

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMacNamePreferenceAndFailureBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]string
		want   string
		keys   []string
	}{
		{"explicit host", map[string]string{"HostName": " chosen ", "LocalHostName": "local", "ComputerName": "computer"}, "chosen", []string{"HostName"}},
		{"local host", map[string]string{"LocalHostName": "local", "ComputerName": "computer"}, "local", []string{"HostName", "LocalHostName"}},
		{"computer", map[string]string{"ComputerName": "computer"}, "computer", []string{"HostName", "LocalHostName", "ComputerName"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var keys []string
			got, err := macName(context.Background(), func(_ context.Context, key string) (string, error) {
				keys = append(keys, key)
				if v, ok := tc.values[key]; ok {
					return v, nil
				}
				return "", errUnset
			})
			if err != nil || got != tc.want || !reflect.DeepEqual(keys, tc.keys) {
				t.Fatalf("name/order: %q %v %v", got, err, keys)
			}
		})
	}
	var keys []string
	wantErr := errors.New("scutil failed")
	_, err := macName(context.Background(), func(_ context.Context, key string) (string, error) { keys = append(keys, key); return "", wantErr })
	if !errors.Is(err, wantErr) || len(keys) != 1 {
		t.Fatalf("failed source silently fell to a lower name: %v %v", keys, err)
	}
}
