// Copyright (c) 2014 AKUALAB INC., All rights reserved.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

// Train various collaborative filtering algorithms using a training set.

import (
	"fmt"
	"log"
	"runtime"
	"time"

	"github.com/akualab/coap"
	"github.com/akualab/coap/store"
)

type Options struct {
	db             *store.Store
	chunkSize      int
	regularization float64
	learnRate      float64
	numFactors     int
	meanNorm       bool    // subtract bias: mu+bi+bu
	alpha          float64 // cont to combine global mean
}

// gets data from DB
// returns chunks of observations in a slice.
func movieFunc(idx uint64, ctx *coap.Context) (coap.Value, error) {
	opt := ctx.Options.(*Options)
	db := opt.db
	n := uint64(opt.chunkSize)
	s := make([]Obs, 0, n)
	var base uint64 = idx * n
	var i uint64
	for ; i < n; i++ {
		v, err := db.Get(base + i)
		if err == store.ErrKeyNotFound {
			return s, coap.ErrEndOfArray
		}
		s = append(s, v.(Obs))
	}
	return s, nil
}

// Computes various global statistics on the data set.
func cfFunc(idx uint64, ctx *coap.Context) (coap.Value, error) {
	opt := ctx.Options.(*Options)
	in, err := ctx.Inputs()[0](idx)
	if err != nil && err != coap.ErrEndOfArray {
		return nil, err // something is wrong
	}
	if in == nil {
		return nil, coap.ErrEndOfArray
	}
	s := in.([]Obs)
	cf := NewCF(opt.alpha)
	for _, v := range s {
		r := v.Rating
		if r < 1 || r > 5 {
			return nil, fmt.Errorf("rating out of range: %d", r)
		}
		cf.Update(v.User, v.Item, v.Rating)
	}
	return cf, err // err may be ErrEndOfArray
}

// Aggregate CF.
func aggCFFunc(idx uint64, ctx *coap.Context) (coap.Value, error) {
	opt := ctx.Options.(*Options)
	if idx > 0 {
		return nil, coap.ErrEndOfArray
	}
	cf := NewCF(opt.alpha)
	ch := ctx.Inputs()[0].MapAll(0)
	for {
		v, ok := <-ch
		if !ok {
			return cf, nil
		}
		q := v.(*CF)
		cf.Reduce(q)
	}
}

// Matrix factorization.
func mfFunc(idx uint64, ctx *coap.Context) (coap.Value, error) {
	opt := ctx.Options.(*Options)

	// input 0 has chunks of data
	chunks := ctx.Inputs()[0] // chunks of observations

	// input 1 has aggregated data from a previous pass through the entire data set
	in1, e1 := ctx.Inputs()[1](0) // aggregated data
	if e1 != nil {
		return nil, e1
	}
	cf := in1.(*CF)
	cf.InitMF(opt.numFactors, opt.learnRate, opt.regularization, opt.meanNorm)
	// Now we can iterate over chunks and for each chunk.
	var c, iter uint64
	for ; iter < idx; iter++ {
		log.Printf("GD iter: %d", iter)
		for c = 0; ; c++ {
			in0, err := chunks(c)
			if err == coap.ErrEndOfArray {
				break
			}
			s := in0.([]Obs)
			for _, v := range s {
				cf.GDUpdate(v.User, v.Item, v.Rating)
			}
		}
	}
	return cf, nil
}

// the app
func TrainCF(dbName string, chunkSize int) *CF {

	var db *store.Store
	var err error

	db, err = store.NewStore(dbName)
	fatalIf(err)
	defer db.Close()

	var numGDIterations uint64 = 40
	opt := &Options{
		db:             db,
		chunkSize:      chunkSize,
		regularization: 0.1,
		learnRate:      0.01,
		numFactors:     4,
		meanNorm:       false,
		alpha:          1,
	}

	app := coap.NewApp(dbName)
	dataChunk := app.AddSource(movieFunc, opt, nil)
	cfProc := app.Add(cfFunc, opt, dataChunk)
	aggCFProc := app.Add(aggCFFunc, opt, cfProc)

	mfProc := app.Add(mfFunc, opt, dataChunk, aggCFProc)

	log.Printf("num logical CPUs: %d", runtime.NumCPU())
	start := time.Now()
	y, ey := mfProc(numGDIterations) // the index is the # iterations
	if ey != nil {
		log.Fatal(ey)
	}
	end := time.Now()
	d := end.Sub(start)
	log.Printf("train duration: %v", d)

	return y.(*CF)
}
