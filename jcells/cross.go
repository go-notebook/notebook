package jcells

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/go-embedded-julia/julia/interp"
)

// The variables that cross between Julia and the other languages are the ones
// the kernel's Get and Set convert (go-embedded-julia#60), copied both ways:
//
//	julia                               Go
//	Int64                               int64 (Set also takes an int)
//	Float64                             float64
//	Bool                                bool
//	String                              string
//	Vector{Float64}, Vector{Int64}      []float64, []int64
//	Matrix{Float64}, Matrix{Int64}      interp.Matrix[float64], interp.Matrix[int64],
//	                                    column-major, element (i, j) at Data[i+j*Rows]
//
// Nothing else crosses: a Dict, a struct, a Vector{String} or a function stays
// in Julia. A Go cell that reads such a variable is deferred with a message that
// names its Julia type; a Julia cell that reads a Go variable of another type
// than the table's fails with a message that names the Go type.

// matrixPath is the import path of interp.Matrix, as a Go type is written in
// the notes (gen.Options.RubyTypes syntax).
const matrixPath = "github.com/go-embedded-julia/julia/interp"

// Unsupported is the prefix of the note of a variable whose Julia type does not
// cross: Unsupported + the Julia type.
const Unsupported = "!"

// GoType is the Go type of a value of the table, in the notes' syntax, and
// false for a value that is not one.
func GoType(v any) (string, bool) {
	switch v.(type) {
	case int64:
		return "int64", true
	case float64:
		return "float64", true
	case bool:
		return "bool", true
	case string:
		return "string", true
	case []float64:
		return "[]float64", true
	case []int64:
		return "[]int64", true
	case interp.Matrix[float64]:
		return "{" + matrixPath + "}.Matrix[float64]", true
	case interp.Matrix[int64]:
		return "{" + matrixPath + "}.Matrix[int64]", true
	}
	return "", false
}

// arrayLen is how many elements of a Julia array a Go value would make, 0 for a
// value that is no slice or matrix (a scalar, a string). It is measured before
// anything is copied, for the total of a cell's inputs, which the kernel's Set
// (per array, since v0.18.0 before it converts) does not bound.
func arrayLen(x any) int64 {
	switch v := x.(type) {
	case []float64:
		return int64(len(v))
	case []int64:
		return int64(len(v))
	case []int:
		return int64(len(v))
	case interp.Matrix[float64]:
		return int64(len(v.Data))
	case interp.Matrix[int64]:
		return int64(len(v.Data))
	}
	return 0
}

// widen is x as a value the kernel takes: a []int is an []int64 (int is int64 on
// a 64-bit machine, and widens on a 32-bit one), the rest as it is.
func widen(x any) any {
	if v, ok := x.([]int); ok {
		out := make([]int64, len(v))
		for i, e := range v {
			out[i] = int64(e)
		}
		return out
	}
	return x
}

// give hands the Go variables a cell reads to the kernel. A holder that is
// empty (an interface holding nothing: no cell has set it) is skipped, and so is
// a Julia cell's own variable the kernel already holds (IfAbsent). The arrays
// given are bounded: none may have more than the VM's MaxMemoryElems elements,
// and all of them together stay within the cell's memory budget (MaxAlloc, in
// elements), checked before any is copied. A Go copy of a slice is 16 bytes an
// element, so K inputs of the per-array cap would cost K times 256 MiB outside
// the budget.
func (s *Session) give(in []Var) error {
	budget, perArray := s.options().MaxAlloc, int64(s.k.VM().MaxMemoryElems)
	var total int64
	for _, v := range in {
		dst := reflect.ValueOf(v.Ptr).Elem()
		if dst.Kind() == reflect.Interface && dst.IsNil() {
			continue
		}
		// the kernel holds it (a value of a type that does not cross too). An error
		// (busy, closed) is not an answer: Set below fails with the same one.
		if has, err := s.k.Has(v.Name); v.IfAbsent && err == nil && has {
			continue
		}
		x := dst.Interface()
		n := arrayLen(x)
		if n > perArray {
			return cellError(fmt.Sprintf("the Julia cell reads %s, a Go %T of %d elements; an array has at most %d elements here", v.Name, x, n, perArray))
		}
		if total += n; budget > 0 && total > budget {
			return cellError(fmt.Sprintf("the Julia cell reads more than its memory budget of %d array elements: %s brings the total to %d", budget, v.Name, total))
		}
		if err := s.k.Set(v.Name, widen(x)); err != nil {
			return cellError(fmt.Sprintf("the Julia cell reads %s, a Go %T that Julia cannot take: %v; "+
				"Julia takes int, int64, float64, bool, string, []int, []int64, []float64 and interp.Matrix of float64 or int64", v.Name, x, err))
		}
	}
	return nil
}

// take reads the variables a cell assigned into their Go holders and notes
// their Go types. A variable the cell did not assign is skipped; one of a type
// that does not cross is noted with Unsupported and the Julia type, and its
// holder is left as it was; a holder of another type than the value's (the
// variable changed type) is left too, and the note makes the next program
// declare the new type.
func (s *Session) take(c Cell) {
	note := c.Note
	if note == nil {
		note = func(string, string) {}
	}
	for _, v := range c.Out {
		val, ok, err := s.k.Get(v.Name)
		if err != nil {
			// a value of a type that does not cross (the kernel is not busy or
			// closed here: this cell has just run in it)
			why, ce := err.Error(), (*interp.ConversionError)(nil)
			if errors.As(err, &ce) {
				why = ce.Julia
			}
			note(v.Name, Unsupported+why)
			continue
		}
		if !ok {
			continue
		}
		typ, _ := GoType(val) // every value Get answers is of the table
		note(v.Name, typ)
		if dst, x := reflect.ValueOf(v.Ptr).Elem(), reflect.ValueOf(val); x.Type().AssignableTo(dst.Type()) {
			dst.Set(x)
		}
	}
}
