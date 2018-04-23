// Copyright 2016 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package distsqlrun

import (
	"context"
	"fmt"
	"testing"

	"github.com/cockroachdb/cockroach/pkg/settings/cluster"
	"github.com/cockroachdb/cockroach/pkg/sql/sem/tree"
	"github.com/cockroachdb/cockroach/pkg/sql/sqlbase"
	"github.com/cockroachdb/cockroach/pkg/util/encoding"
	"github.com/cockroachdb/cockroach/pkg/util/leaktest"
	"sort"
)

type mergeJoinerTestCase struct {
	spec          MergeJoinerSpec
	outCols       []uint32
	leftTypes     []sqlbase.ColumnType
	leftInput     sqlbase.EncDatumRows
	rightTypes    []sqlbase.ColumnType
	rightInput    sqlbase.EncDatumRows
	expectedTypes []sqlbase.ColumnType
	expected      sqlbase.EncDatumRows
}

func TestMergeJoiner(t *testing.T) {
	defer leaktest.AfterTest(t)()

	testCases := joinerTestCases()

	// Add INTERSECT ALL cases with MergeJoinerSpecs.
	for _, tc := range intersectAllTestCases() {
		testCases = append(testCases, setOpTestCaseToJoinerTestCase(tc))
	}

	// Add EXCEPT ALL cases with MergeJoinerSpecs.
	for _, tc := range exceptAllTestCases() {
		testCases = append(testCases, setOpTestCaseToJoinerTestCase(tc))
	}

	for _, c := range testCases {
		t.Run("", func(t *testing.T) {
			st := cluster.MakeTestingClusterSettings()
			evalCtx := tree.MakeTestingEvalContext(st)

			// Ensure that test inputs are sorted on the equality columns.
			sortInputByEqCols(c.leftInput, c.leftEqCols, c.leftTypes, &evalCtx)
			sortInputByEqCols(c.rightInput, c.rightEqCols, c.rightTypes, &evalCtx)

			leftInput := NewRowBuffer(c.leftTypes, c.leftInput, RowBufferArgs{})
			rightInput := NewRowBuffer(c.rightTypes, c.rightInput, RowBufferArgs{})
			out := &RowBuffer{}
			defer evalCtx.Stop(context.Background())
			flowCtx := FlowCtx{
				Settings: st,
				EvalCtx:  evalCtx,
			}

			leftOrdering := make(sqlbase.ColumnOrdering, len(c.leftEqCols))
			for i, col := range c.leftEqCols {
				leftOrdering[i] = sqlbase.ColumnOrderInfo{ColIdx: int(col), Direction: encoding.Ascending}
			}
			rightOrdering := make(sqlbase.ColumnOrdering, len(c.rightEqCols))
			for i, col := range c.rightEqCols {
				rightOrdering[i] = sqlbase.ColumnOrderInfo{ColIdx: int(col), Direction: encoding.Ascending}
			}
			ms := &MergeJoinerSpec{
				LeftOrdering:  convertToSpecOrdering(leftOrdering),
				RightOrdering: convertToSpecOrdering(rightOrdering),
				Type:          c.joinType,
				OnExpr:        c.onExpr,
			}
			post := PostProcessSpec{Projection: true, OutputColumns: c.outCols}
			m, err := newMergeJoiner(&flowCtx, ms, leftInput, rightInput, &post, out)
			if err != nil {
				t.Fatal(err)
			}

			m.Run(context.Background(), nil /* wg */)

			if !out.ProducerClosed {
				t.Fatalf("output RowReceiver not closed")
			}

			outTypes := m.OutputTypes()
			if err := checkExpectedRows(outTypes, c.expected, out); err != nil {
				t.Error(err)
			}
		})
	}
}

func sortInputByEqCols(
	input sqlbase.EncDatumRows,
	eqCols []uint32,
	types []sqlbase.ColumnType,
	evalCtx *tree.EvalContext,
) {
	rowOrdering := make(sqlbase.ColumnOrdering, len(eqCols))
	// Assume all test input is in ascending order.
	for i, c := range eqCols {
		rowOrdering[i] = sqlbase.ColumnOrderInfo{ColIdx: int(c), Direction: encoding.Ascending}
	}
	sorter := func(i, j int) bool {
		da := &sqlbase.DatumAlloc{}
		iEqualityCols := make(sqlbase.EncDatumRow, len(eqCols))
		for k, col := range eqCols {
			iEqualityCols[k] = input[i][col]
		}
		jEqualityCols := make(sqlbase.EncDatumRow, len(eqCols))
		for k, col := range eqCols {
			jEqualityCols[k] = input[j][col]
		}
		eqTypes := make([]sqlbase.ColumnType, len(c.leftEqCols))
		for k, col := range eqCols {
			eqTypes[k] = types[col]
		}
		cmp, _ := iEqualityCols.Compare(types, da, rowOrdering, evalCtx, jEqualityCols)
		return cmp == -1
	}
	sort.Slice(input, sorter)
}

// Test that the joiner shuts down fine if the consumer is closed prematurely.
func TestConsumerClosed(t *testing.T) {
	defer leaktest.AfterTest(t)()

	columnTypeInt := sqlbase.ColumnType{SemanticType: sqlbase.ColumnType_INT}
	v := [10]sqlbase.EncDatum{}
	for i := range v {
		v[i] = sqlbase.DatumToEncDatum(columnTypeInt, tree.NewDInt(tree.DInt(i)))
	}

	spec := MergeJoinerSpec{
		LeftOrdering: convertToSpecOrdering(
			sqlbase.ColumnOrdering{
				{ColIdx: 0, Direction: encoding.Ascending},
			}),
		RightOrdering: convertToSpecOrdering(
			sqlbase.ColumnOrdering{
				{ColIdx: 0, Direction: encoding.Ascending},
			}),
		Type: sqlbase.InnerJoin,
		// Implicit @1 = @2 constraint.
	}
	outCols := []uint32{0}
	leftTypes := oneIntCol
	rightTypes := oneIntCol

	testCases := []struct {
		typ       sqlbase.JoinType
		leftRows  sqlbase.EncDatumRows
		rightRows sqlbase.EncDatumRows
	}{
		{
			typ: sqlbase.InnerJoin,
			// Implicit @1 = @2 constraint.
			leftRows: sqlbase.EncDatumRows{
				{v[0]},
			},
			rightRows: sqlbase.EncDatumRows{
				{v[0]},
			},
		},
		{
			typ: sqlbase.LeftOuterJoin,
			// Implicit @1 = @2 constraint.
			leftRows: sqlbase.EncDatumRows{
				{v[0]},
			},
			rightRows: sqlbase.EncDatumRows{},
		},
		{
			typ: sqlbase.RightOuterJoin,
			// Implicit @1 = @2 constraint.
			leftRows: sqlbase.EncDatumRows{},
			rightRows: sqlbase.EncDatumRows{
				{v[0]},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.typ.String() /* name */, func(t *testing.T) {
			leftInput := NewRowBuffer(leftTypes, tc.leftRows, RowBufferArgs{})
			rightInput := NewRowBuffer(rightTypes, tc.rightRows, RowBufferArgs{})

			// Create a consumer and close it immediately. The mergeJoiner should find out
			// about this closer the first time it attempts to push a row.
			out := &RowBuffer{}
			out.ConsumerDone()

			st := cluster.MakeTestingClusterSettings()
			evalCtx := tree.MakeTestingEvalContext(st)
			defer evalCtx.Stop(context.Background())
			flowCtx := FlowCtx{
				Settings: st,
				EvalCtx:  evalCtx,
			}
			post := PostProcessSpec{Projection: true, OutputColumns: outCols}
			m, err := newMergeJoiner(&flowCtx, &spec, leftInput, rightInput, &post, out)
			if err != nil {
				t.Fatal(err)
			}

			m.Run(context.Background(), nil /* wg */)

			if !out.ProducerClosed {
				t.Fatalf("output RowReceiver not closed")
			}
		})
	}
}

func BenchmarkMergeJoiner(b *testing.B) {
	ctx := context.Background()
	st := cluster.MakeTestingClusterSettings()
	evalCtx := tree.MakeTestingEvalContext(st)
	defer evalCtx.Stop(ctx)
	flowCtx := &FlowCtx{
		Settings: st,
		EvalCtx:  evalCtx,
	}

	spec := &MergeJoinerSpec{
		LeftOrdering: convertToSpecOrdering(
			sqlbase.ColumnOrdering{
				{ColIdx: 0, Direction: encoding.Ascending},
			}),
		RightOrdering: convertToSpecOrdering(
			sqlbase.ColumnOrdering{
				{ColIdx: 0, Direction: encoding.Ascending},
			}),
		Type: sqlbase.InnerJoin,
		// Implicit @1 = @2 constraint.
	}
	post := &PostProcessSpec{}
	disposer := &RowDisposer{}

	const numCols = 1
	for _, inputSize := range []int{0, 1 << 2, 1 << 4, 1 << 8, 1 << 12, 1 << 16} {
		b.Run(fmt.Sprintf("InputSize=%d", inputSize), func(b *testing.B) {
			rows := makeIntRows(inputSize, numCols)
			leftInput := NewRepeatableRowSource(oneIntCol, rows)
			rightInput := NewRepeatableRowSource(oneIntCol, rows)
			b.SetBytes(int64(8 * inputSize * numCols * 2))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m, err := newMergeJoiner(flowCtx, spec, leftInput, rightInput, post, disposer)
				if err != nil {
					b.Fatal(err)
				}
				m.Run(context.Background(), nil /* wg */)
				leftInput.Reset()
				rightInput.Reset()
			}
		})
	}
}
