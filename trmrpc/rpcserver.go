// Package trmrpc contains the proto files, generated code and server logic
// for the terminator's grpc server which serves requests for close
// recommendations.
//
// The Terminator server interface is implemented by the RPCServer struct.
// To keep this file readable, each function implemented by the interface
// has a file named after the function call which contains rpc parsing
// code for the request and response. If the call requires extensive
// additional logic, and unexported function with the same name should
// be created in this file as well.
package trmrpc

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/lightninglabs/terminator/recommend"
	"github.com/lightninglabs/terminator/revenue"
	"github.com/lightningnetwork/lnd/lnrpc"
	"google.golang.org/grpc"
)

// RPCServer implements the terminator service, serving requests over grpc.
type RPCServer struct {
	// To be used atomically.
	started int32

	// To be used atomically.
	stopped int32

	// cfg contains closures and settings required for operation.
	cfg *Config

	// grpcServer is the main gRPC RPCServer that this RPC server will
	// register itself with and accept client requests from.
	grpcServer *grpc.Server

	// rpcListener is the to use when starting the grpc server.
	rpcListener net.Listener

	wg sync.WaitGroup
}

// Config provides closures and settings required to run the rpc server.
type Config struct {
	// LightningClient is a client which can be used to query lnd.
	LightningClient lnrpc.LightningClient

	// RPCListen is the address:port that the rpc server should listen
	// on.
	RPCListen string
}

// wrapListChannels wraps the listchannels call to lnd, with a publicOnly bool
// that can be used to toggle whether private channels are included.
func (c *Config) wrapListChannels(ctx context.Context,
	publicOnly bool) func() ([]*lnrpc.Channel, error) {

	return func() (channels []*lnrpc.Channel, e error) {
		resp, err := c.LightningClient.ListChannels(
			ctx,
			&lnrpc.ListChannelsRequest{
				PublicOnly: publicOnly,
			},
		)
		if err != nil {
			return nil, err
		}

		return resp.Channels, nil
	}
}

// NewRPCServer returns a server which will listen for rpc requests on the
// rpc listen address provided. Note that the server returned is not running,
// and should be started using Start().
func NewRPCServer(cfg *Config) *RPCServer {
	var opts []grpc.ServerOption
	grpcServer := grpc.NewServer(opts...)

	return &RPCServer{
		cfg:        cfg,
		grpcServer: grpcServer,
	}
}

// Start starts the listener and server.
func (s *RPCServer) Start() error {
	if atomic.AddInt32(&s.started, 1) != 1 {
		return nil
	}

	// Start the gRPC RPCServer listening for HTTP/2 connections.
	log.Info("Starting gRPC listener")
	grpcListener, err := net.Listen("tcp", s.cfg.RPCListen)
	if err != nil {
		return fmt.Errorf("RPC RPCServer unable to listen on %v",
			s.cfg.RPCListen)

	}
	s.rpcListener = grpcListener

	RegisterTerminatorServerServer(s.grpcServer, s)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.grpcServer.Serve(s.rpcListener); err != nil {
			log.Errorf("could not serve grpc server: %v", err)
		}
	}()

	return nil
}

// Stop stops the grpc listener and server.
func (s *RPCServer) Stop() error {
	if atomic.AddInt32(&s.stopped, 1) != 1 {
		return nil
	}

	// Stop the grpc server and wait for all go routines to terminate.
	s.grpcServer.Stop()
	s.wg.Wait()

	return nil
}

// CloseRecommendations provides a set of close recommendations for the
// current set of open channels.
func (s *RPCServer) CloseRecommendations(ctx context.Context,
	req *CloseRecommendationsRequest) (*CloseRecommendationsResponse,
	error) {

	cfg := parseRequest(ctx, s.cfg, req)

	report, err := recommend.CloseRecommendations(cfg)
	if err != nil {
		return nil, err
	}

	return parseResponse(report), nil
}

// RevenueReport returns a pairwise revenue report for a channel
// over the period requested.
func (s *RPCServer) RevenueReport(ctx context.Context,
	req *RevenueReportRequest) (*RevenueReportResponse, error) {

	revenueConfig := parseRevenueRequest(ctx, s.cfg, req)

	report, err := revenue.GetRevenueReport(revenueConfig)
	if err != nil {
		return nil, err
	}

	return rpcRevenueResponse(req.GetChanPoints(), report)
}
