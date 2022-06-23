package frdrpcserver

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/lightningnetwork/lnd/kvdb"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/macaroons"
	"github.com/lightningnetwork/lnd/rpcperms"
	"go.etcd.io/bbolt"
	"google.golang.org/grpc"
	"gopkg.in/macaroon-bakery.v2/bakery"

	"github.com/lightninglabs/faraday/frdrpcserver/perms"
)

const (
	// faradayMacaroonLocation is the value we use for the faraday
	// macaroons' "Location" field when baking them.
	faradayMacaroonLocation = "faraday"

	// macDatabaseOpenTimeout is how long we wait for acquiring the lock on
	// the macaroon database before we give up with an error.
	macDatabaseOpenTimeout = time.Second * 5
)

var (

	// allPermissions is the list of all existing permissions that exist
	// for faraday's RPC. The default macaroon that is created on startup
	// contains all these permissions and is therefore equivalent to lnd's
	// admin.macaroon but for faraday.
	allPermissions = []bakery.Op{{
		Entity: "recommendation",
		Action: "read",
	}, {
		Entity: "report",
		Action: "read",
	}, {
		Entity: "audit",
		Action: "read",
	}, {
		Entity: "insights",
		Action: "read",
	}, {
		Entity: "rates",
		Action: "read",
	}}

	// macDbDefaultPw is the default encryption password used to encrypt the
	// faraday macaroon database. The macaroon service requires us to set a
	// non-nil password so we set it to an empty string. This will cause the
	// keys to be encrypted on disk but won't provide any security at all as
	// the password is known to anyone.
	//
	// TODO(guggero): Allow the password to be specified by the user. Needs
	// create/unlock calls in the RPC. Using a password should be optional
	// though.
	macDbDefaultPw = []byte("")
)

// startMacaroonService starts the macaroon validation service, creates or
// unlocks the macaroon database and creates the default macaroon if it doesn't
// exist yet.
func (s *RPCServer) startMacaroonService(createDefaultMacaroonFile bool) error {
	var err error
	s.macaroonDB, err = kvdb.GetBoltBackend(&kvdb.BoltBackendConfig{
		DBPath:     s.cfg.FaradayDir,
		DBFileName: "macaroons.db",
		DBTimeout:  macDatabaseOpenTimeout,
	})
	if err == bbolt.ErrTimeout {
		return fmt.Errorf("error while trying to open %s/%s: "+
			"timed out after %v when trying to obtain exclusive "+
			"lock - make sure no other faraday daemon process "+
			"(standalone or embedded in lightning-terminal) is "+
			"running", s.cfg.FaradayDir, "macaroons.db",
			macDatabaseOpenTimeout)
	}
	if err != nil {
		return fmt.Errorf("unable to load macaroon db: %v", err)
	}

	// Create the macaroon authentication/authorization service.
	s.macaroonService, err = macaroons.NewService(
		s.macaroonDB, faradayMacaroonLocation, false,
		macaroons.IPLockChecker,
	)
	if err != nil {
		return fmt.Errorf("unable to set up macaroon authentication: "+
			"%v", err)
	}

	// Try to unlock the macaroon store with the private password.
	err = s.macaroonService.CreateUnlock(&macDbDefaultPw)
	if err != nil {
		return fmt.Errorf("unable to unlock macaroon DB: %v", err)
	}

	// There are situations in which we don't want a macaroon to be created
	// on disk (for example when running inside LiT stateless integrated
	// mode). For any other cases, we create macaroon files for the faraday
	// CLI in the default directory.
	if createDefaultMacaroonFile && !lnrpc.FileExists(s.cfg.MacaroonPath) {
		// We don't offer the ability to rotate macaroon root keys yet,
		// so just use the default one since the service expects some
		// value to be set.
		idCtx := macaroons.ContextWithRootKeyID(
			context.Background(), macaroons.DefaultRootKeyID,
		)

		// We only generate one default macaroon that contains all
		// existing permissions (equivalent to the admin.macaroon in
		// lnd). Custom macaroons can be created through the bakery
		// RPC.
		faradayMac, err := s.macaroonService.Oven.NewMacaroon(
			idCtx, bakery.LatestVersion, nil, allPermissions...,
		)
		if err != nil {
			return err
		}
		frdMacBytes, err := faradayMac.M().MarshalBinary()
		if err != nil {
			return err
		}
		err = ioutil.WriteFile(s.cfg.MacaroonPath, frdMacBytes, 0644)
		if err != nil {
			if err := os.Remove(s.cfg.MacaroonPath); err != nil {
				log.Errorf("Unable to remove %s: %v",
					s.cfg.MacaroonPath, err)
			}
			return err
		}
	}

	return nil
}

// stopMacaroonService closes the macaroon database.
func (s *RPCServer) stopMacaroonService() error {
	var shutdownErr error
	if err := s.macaroonService.Close(); err != nil {
		log.Errorf("Error closing macaroon service: %v", err)
		shutdownErr = err
	}

	if err := s.macaroonDB.Close(); err != nil {
		log.Errorf("Error closing macaroon DB: %v", err)
		shutdownErr = err
	}

	return shutdownErr
}

// macaroonInterceptor creates gRPC server options with the macaroon security
// interceptors.
func (s *RPCServer) macaroonInterceptor() ([]grpc.ServerOption, error) {
	interceptor := rpcperms.NewInterceptorChain(log, false, nil)

	err := interceptor.Start()
	if err != nil {
		return nil, err
	}

	interceptor.SetWalletUnlocked()
	interceptor.AddMacaroonService(s.macaroonService)

	for method, permissions := range perms.RequiredPermissions {
		err := interceptor.AddPermission(method, permissions)
		if err != nil {
			return nil, err
		}
	}

	unaryInterceptor := interceptor.MacaroonUnaryServerInterceptor()
	streamInterceptor := interceptor.MacaroonStreamServerInterceptor()
	return []grpc.ServerOption{
		grpc.UnaryInterceptor(unaryInterceptor),
		grpc.StreamInterceptor(streamInterceptor),
	}, nil
}