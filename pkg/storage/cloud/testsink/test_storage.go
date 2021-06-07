// Copyright 2021 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

// TODO: Rename teestsink to TestStorage
package testsink

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"net/url"

	"github.com/cockroachdb/cockroach/pkg/base"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/storage/cloud"
	"github.com/cockroachdb/errors"
)

type testStorage struct {
	files map[string][]byte
}

// This test uses a singleton instance that is reused.
var testStorageSingleton cloud.ExternalStorage = &testStorage{}

func makeTestStorage(
	ctx context.Context, args cloud.ExternalStorageContext, dest roachpb.ExternalStorage,
) (cloud.ExternalStorage, error) {
	return testStorageSingleton, nil
}

// Conf implements cloud.ExternalStorage.
func (s *testStorage) Conf() roachpb.ExternalStorage {
	return roachpb.ExternalStorage{
		Provider: roachpb.ExternalStorageProvider_test,
	}
}

// Conf implements cloud.ExternalStorage.
func (s *testStorage) ExternalIOConf() base.ExternalIODirConfig {
	panic("not implemented")
}

// Settings implements cloud.ExternalStorage.
func (s *testStorage) Settings() *cluster.Settings {
	panic("not implemented")
}

func (s *testStorage) Writer(ctx context.Context, basename string) (io.WriteCloser, error) {
	return cloud.BackgroundPipe(ctx, func(ctx context.Context, r io.Reader) error {
		data, err := ioutil.ReadAll(r)
		if err != nil {
			return err
		}
		s.files[basename] = data
		
		// TODO: Add latency.
		return nil
	}), nil
}

// ReadFile is shorthand for ReadFileAt with offset 0.
func (s *testStorage) ReadFile(ctx context.Context, basename string) (io.ReadCloser, error) {
	r, _, err := s.ReadFileAt(ctx, basename, 0)
	return r, err
}

// ReadFileAt opens a reader at the requested offset.
func (s *testStorage) ReadFileAt(
	ctx context.Context, basename string, offset int64,
) (io.ReadCloser, int64, error) {
	size, err := s.Size(ctx, basename)
	if err != nil {
		return nil, 0, err
	}
	return &cloud.ResumingReader{
		Ctx: ctx,
		Opener: func(ctx context.Context, pos int64) (io.ReadCloser, error) {
			return s.openReaderAt(basename, pos)
		},
		Pos:    offset,
	}, size, nil
}

func (s *testStorage) openReaderAt(basename string, pos int64) (io.ReadCloser, error) {
	// TODO: Add latency.
	data, ok := s.files[basename]
	if !ok {
		return nil, cloud.ErrFileDoesNotExist
	}
	if pos != 0 {
		data = data[pos:]
	}

	return ioutil.NopCloser(bytes.NewReader(data)), nil
}


func (s *testStorage) ListFiles(ctx context.Context, pattern string) ([]string, error) {
	var fileList []string
	for fileName := range s.files {
		fileList = append(fileList, fileName)
	}

	if cloud.ContainsGlob(pattern) {
		return nil, errors.New("testStorage cannot look up patterns")
	}

	return fileList, nil
}

func (s *testStorage) Delete(ctx context.Context, basename string) error {
	s.files[basename] = nil
	return nil
}

func (s *testStorage) Size(ctx context.Context, basename string) (int64, error) {
	return int64(len(s.files[basename])), nil
}

func (s *testStorage) Close() error {
	return nil
}

func parseTestURL(_ cloud.ExternalStorageURIContext, _ *url.URL) (roachpb.ExternalStorage, error) {
	return roachpb.ExternalStorage{Provider: roachpb.ExternalStorageProvider_test}, nil
}

func init() {
	cloud.RegisterExternalStorageProvider(roachpb.ExternalStorageProvider_test,
		parseTestURL, makeTestStorage, cloud.RedactedParams(), "test")
}
