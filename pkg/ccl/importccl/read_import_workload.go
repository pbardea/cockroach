// Copyright 2019 The Cockroach Authors.
//
// Licensed as a CockroachDB Enterprise file under the Cockroach Community
// License (the "License"); you may not use this file except in compliance with
// the License. You may obtain a copy of the License at
//
//     https://github.com/cockroachdb/cockroach/blob/master/licenses/CCL.txt

package importccl

import (
	"context"
	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/security"
	"github.com/cockroachdb/cockroach/pkg/sql/catalog"
	"github.com/cockroachdb/cockroach/pkg/sql/row"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/storage/cloud"
	"github.com/cockroachdb/cockroach/pkg/util/ctxgroup"
	"github.com/cockroachdb/cockroach/pkg/workload"
	"github.com/cockroachdb/errors"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

type workloadReader struct {
	evalCtx *tree.EvalContext
	table   catalog.TableDescriptor
	kvCh    chan row.KVBatch
}

var _ inputConverter = &workloadReader{}

func newWorkloadReader(
	kvCh chan row.KVBatch, table catalog.TableDescriptor, evalCtx *tree.EvalContext,
) *workloadReader {
	return &workloadReader{evalCtx: evalCtx, table: table, kvCh: kvCh}
}

func (w *workloadReader) start(ctx ctxgroup.Group) {
}

func (w *workloadReader) readFiles(
	ctx context.Context,
	dataFiles map[int32]string,
	_ map[int32]int64,
	_ roachpb.IOFileFormat,
	_ cloud.ExternalStorageFactory,
	_ security.SQLUsername,
) error {

	wcs := make([]*workload.WorkloadKVConverter, 0, len(dataFiles))
	for fileID, fileName := range dataFiles {
		conf, err := parseWorkloadConfig(fileName)
		if err != nil {
			return err
		}
		meta, err := workload.Get(conf.Generator)
		if err != nil {
			return err
		}
		// Different versions of the workload could generate different data, so
		// disallow this.
		if meta.Version != conf.Version {
			return errors.Errorf(
				`expected %s version "%s" but got "%s"`, meta.Name, conf.Version, meta.Version)
		}
		gen := meta.New()
		if f, ok := gen.(workload.Flagser); ok {
			flags := f.Flags()
			if err := flags.Parse(conf.Flags); err != nil {
				return errors.Wrapf(err, `parsing parameters %s`, strings.Join(conf.Flags, ` `))
			}
		}
		var t workload.Table
		for _, tbl := range gen.Tables() {
			if tbl.Name == conf.Table {
				t = tbl
				break
			}
		}
		if t.Name == `` {
			return errors.Errorf(`unknown table %s for generator %s`, conf.Table, meta.Name)
		}

		wc := workload.NewWorkloadKVConverter(
			fileID, w.table, t.InitialRows, int(conf.BatchBegin), int(conf.BatchEnd), w.kvCh)
		wcs = append(wcs, wc)
	}

	for _, wc := range wcs {
		if err := ctxgroup.GroupWorkers(ctx, runtime.GOMAXPROCS(0), func(ctx context.Context, _ int) error {
			evalCtx := w.evalCtx.Copy()
			return wc.Worker(ctx, evalCtx)
		}); err != nil {
			return err
		}
	}
	return nil
}

var errNotWorkloadURI = errors.New("not a workload URI")

// parseWorkloadConfig parses a workload config URI to a config.
func parseWorkloadConfig(fileName string) (workloadConfig, error) {
	c := workloadConfig{}

	uri, err := url.Parse(fileName)
	if err != nil {
		return c, err
	}

	if uri.Scheme != "workload" {
		return c, errNotWorkloadURI
	}
	pathParts := strings.Split(strings.Trim(uri.Path, `/`), `/`)
	if len(pathParts) != 3 {
		return c, errors.Errorf(
			`path must be of the form /<format>/<generator>/<table>: %s`, uri.Path)
	}
	c.Format, c.Generator, c.Table = pathParts[0], pathParts[1], pathParts[2]
	q := uri.Query()
	if _, ok := q[`version`]; !ok {
		return c, errors.New(`parameter version is required`)
	}
	c.Version = q.Get(`version`)
	q.Del(`version`)
	if s := q.Get(`row-start`); len(s) > 0 {
		q.Del(`row-start`)
		var err error
		if c.BatchBegin, err = strconv.ParseInt(s, 10, 64); err != nil {
			return c, err
		}
	}
	if e := q.Get(`row-end`); len(e) > 0 {
		q.Del(`row-end`)
		var err error
		if c.BatchEnd, err = strconv.ParseInt(e, 10, 64); err != nil {
			return c, err
		}
	}
	for k, vs := range q {
		for _, v := range vs {
			c.Flags = append(c.Flags, `--`+k+`=`+v)
		}
	}
	return c, nil
}

type workloadConfig struct {
	Generator  string
	Version    string
	Table      string
	Flags      []string
	Format     string
	BatchBegin int64
	BatchEnd   int64
}
