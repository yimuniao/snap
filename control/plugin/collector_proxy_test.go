// +build legacy

/*
http://www.apache.org/licenses/LICENSE-2.0.txt


Copyright 2015 Intel Corporation

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package plugin

import (
	"errors"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/intelsdi-x/snap/control/plugin/cpolicy"
	"github.com/intelsdi-x/snap/control/plugin/encoding"
	"github.com/intelsdi-x/snap/control/plugin/rpc"
	"github.com/intelsdi-x/snap/core"
	"github.com/intelsdi-x/snap/grpc/common"
	"github.com/intelsdi-x/snap/pkg/rpcutil"

	. "github.com/smartystreets/goconvey/convey"
	"google.golang.org/grpc"
)

type mockPlugin struct {
}

var mockMetricType = []MetricType{
	*NewMetricType(core.NewNamespace("foo").AddDynamicElement("test", "something dynamic here").AddStaticElement("bar"), time.Now(), nil, "", 1),
	*NewMetricType(core.NewNamespace("foo", "baz"), time.Now(), nil, "", 2),
}

func (p *mockPlugin) GetMetricTypes(cfg ConfigType) ([]MetricType, error) {
	return mockMetricType, nil
}

func (p *mockPlugin) CollectMetrics(mockMetricTypes []MetricType) ([]MetricType, error) {
	for i := range mockMetricTypes {
		if mockMetricTypes[i].Namespace().String() == "/foo/*/bar" {
			mockMetricTypes[i].Namespace_[1].Value = "test"
		}
		mockMetricTypes[i].Timestamp_ = time.Now()
		mockMetricTypes[i].LastAdvertisedTime_ = time.Now()
		mockMetricTypes[i].Data_ = "data"
	}
	return mockMetricTypes, nil
}

func (p *mockPlugin) GetConfigPolicy() (*cpolicy.ConfigPolicy, error) {
	cp := cpolicy.New()
	cpn := cpolicy.NewPolicyNode()
	r1, _ := cpolicy.NewStringRule("username", false, "root")
	r2, _ := cpolicy.NewStringRule("password", true)
	r3, _ := cpolicy.NewBoolRule("bool_rule_default_true", false, true)
	r4, _ := cpolicy.NewBoolRule("bool_rule_default_false", false, false)
	r5, _ := cpolicy.NewIntegerRule("integer_rule", true, 1234)
	r5.SetMaximum(9999)
	r5.SetMinimum(1000)
	r6, _ := cpolicy.NewFloatRule("float_rule", true, 0.1234)
	r6.SetMaximum(.9999)
	r6.SetMinimum(.001)
	cpn.Add(r1, r2, r3, r4, r5, r6)
	ns := []string{"one", "two", "potato"}
	cp.Add(ns, cpn)
	cp.Freeze()

	return cp, nil
}

type mockErrorPlugin struct {
}

func (p *mockErrorPlugin) GetMetricTypes(cfg ConfigType) ([]MetricType, error) {
	return nil, errors.New("Error in get Metric Type")
}

func (p *mockErrorPlugin) CollectMetrics(_ []MetricType) ([]MetricType, error) {
	return nil, errors.New("Error in collect Metric")
}

func (p *mockErrorPlugin) GetConfigPolicy() (*cpolicy.ConfigPolicy, error) {
	return &cpolicy.ConfigPolicy{}, errors.New("Error in get config policy")
}

func TestCollectorProxy(t *testing.T) {
	Convey("Test collector plugin proxy for get metric types ", t, func() {

		logger := log.New(os.Stdout,
			"test: ",
			log.Ldate|log.Ltime|log.Lshortfile)
		mockPlugin := &mockPlugin{}

		mockSessionState := &MockSessionState{
			Encoder:             encoding.NewGobEncoder(),
			listenPort:          "0",
			token:               "abcdef",
			logger:              logger,
			PingTimeoutDuration: time.Millisecond * 100,
			killChan:            make(chan int),
		}
		c := &collectorPluginProxy{
			Plugin:  mockPlugin,
			Session: mockSessionState,
		}
		Convey("Get Metric Types", func() {
			var reply []byte
			c.GetMetricTypes([]byte{}, &reply)
			var mtr GetMetricTypesReply
			err := c.Session.Decode(reply, &mtr)
			So(err, ShouldBeNil)
			So(mtr.MetricTypes[0].Namespace().String(), ShouldResemble, "/foo/*/bar")

		})
		Convey("Get error in Get Metric Type", func() {
			mockErrorPlugin := &mockErrorPlugin{}
			errC := &collectorPluginProxy{
				Plugin:  mockErrorPlugin,
				Session: mockSessionState,
			}
			var reply []byte
			err := errC.GetMetricTypes([]byte{}, &reply)
			So(err.Error(), ShouldResemble, "GetMetricTypes call error : Error in get Metric Type")
		})
		Convey("Collect Metric ", func() {
			args := CollectMetricsArgs{
				MetricTypes: mockMetricType,
			}
			out, err := c.Session.Encode(args)
			So(err, ShouldBeNil)
			var reply []byte
			c.CollectMetrics(out, &reply)
			var mtr CollectMetricsReply
			err = c.Session.Decode(reply, &mtr)
			So(mtr.PluginMetrics[0].Namespace().String(), ShouldResemble, "/foo/test/bar")
			So(mtr.PluginMetrics[0].Namespace()[1].Name, ShouldEqual, "test")

			Convey("Get error in Collect Metric ", func() {
				args := CollectMetricsArgs{
					MetricTypes: mockMetricType,
				}
				mockErrorPlugin := &mockErrorPlugin{}
				errC := &collectorPluginProxy{
					Plugin:  mockErrorPlugin,
					Session: mockSessionState,
				}
				out, err := errC.Session.Encode(args)
				So(err, ShouldBeNil)
				var reply []byte
				err = errC.CollectMetrics(out, &reply)
				So(err, ShouldNotBeNil)
			})

		})

	})
}

func TestGRPCPluginProxy(t *testing.T) {
	port, mockSession, err := startCollectorServer()
	Convey("Start gRPC ", t, func() {
		So(err, ShouldBeNil)
	})

	conn, err := rpcutil.GetClientConnection("127.0.0.1", port)
	Convey("create grpc collector", t, func() {
		So(err, ShouldBeNil)
	})
	// We use collector client here but all the clients contain "plugin" functions
	// and they are all used from the same base.
	client := rpc.NewCollectorClient(conn)
	pingRes, err := client.Ping(context.Background(), &common.Empty{})
	Convey("call ping", t, func() {
		So(pingRes, ShouldResemble, &rpc.PingReply{})
		So(err, ShouldBeNil)
	})

	killRes, err := client.Kill(context.Background(), &rpc.KillRequest{Reason: "testing"})
	Convey("calls kill", t, func() {
		So(killRes, ShouldResemble, &rpc.KillReply{})
		So(err, ShouldBeNil)
		So(<-mockSession.KillChan(), ShouldEqual, 0)
	})

	getConfigPolicyRes, err := client.GetConfigPolicy(context.Background(), &common.Empty{})
	Convey("calls GetConfigPolicy", t, func() {
		So(err, ShouldBeNil)
		So(getConfigPolicyRes, ShouldNotBeNil)
		// string policy
		So(len(getConfigPolicyRes.StringPolicy), ShouldEqual, 1)
		So(len(getConfigPolicyRes.StringPolicy["one.two.potato"].Rules), ShouldEqual, 2)
		// bool policy
		So(len(getConfigPolicyRes.BoolPolicy["one.two.potato"].Rules), ShouldEqual, 2)
		So(
			getConfigPolicyRes.BoolPolicy["one.two.potato"].Rules["bool_rule_default_true"].Default,
			ShouldEqual,
			true,
		)
		// integer policy
		So(len(getConfigPolicyRes.IntegerPolicy["one.two.potato"].Rules), ShouldEqual, 1)
		So(
			getConfigPolicyRes.IntegerPolicy["one.two.potato"].Rules["integer_rule"].Default,
			ShouldEqual,
			1234,
		)
		So(
			getConfigPolicyRes.IntegerPolicy["one.two.potato"].Rules["integer_rule"].Maximum,
			ShouldEqual,
			9999,
		)
		So(
			getConfigPolicyRes.IntegerPolicy["one.two.potato"].Rules["integer_rule"].Minimum,
			ShouldEqual,
			1000,
		)
		// float policy
		So(len(getConfigPolicyRes.FloatPolicy["one.two.potato"].Rules), ShouldEqual, 1)
		So(
			getConfigPolicyRes.FloatPolicy["one.two.potato"].Rules["float_rule"].Default,
			ShouldEqual,
			0.1234,
		)
		So(
			getConfigPolicyRes.FloatPolicy["one.two.potato"].Rules["float_rule"].Maximum,
			ShouldEqual,
			.9999,
		)
		So(
			getConfigPolicyRes.FloatPolicy["one.two.potato"].Rules["float_rule"].Minimum,
			ShouldEqual,
			0.001,
		)
	})
}

func TestGRPCCollectorProxy(t *testing.T) {
	port, _, err := startCollectorServer()
	Convey("Start gRPC ", t, func() {
		So(err, ShouldBeNil)
	})

	conn, err := rpcutil.GetClientConnection("127.0.0.1", port)
	Convey("create grpc collector", t, func() {
		So(err, ShouldBeNil)
	})

	client := rpc.NewCollectorClient(conn)
	Convey("create client", t, func() {
		So(err, ShouldBeNil)
		So(client, ShouldNotBeNil)
	})

	getCollectArg := &rpc.CollectMetricsArg{
		Metrics: []*common.Metric{
			{
				LastAdvertisedTime: common.ToTime(time.Now()),
				Namespace: []*common.NamespaceElement{
					{
						Value: "foo",
					},
					{
						Name:  "something",
						Value: "*",
					},
					{
						Value: "bar",
					},
				},
			},
		},
	}
	getCollectRes, err := client.CollectMetrics(context.Background(), getCollectArg)
	Convey("calls CollectMetrics", t, func() {
		So(err, ShouldBeNil)
		So(getCollectRes, ShouldNotBeNil)
		So(len(getCollectRes.Metrics), ShouldEqual, 1)
		So(getCollectRes.Metrics[0].Namespace[1].Value, ShouldEqual, "test")
		So(getCollectRes.Metrics[0].Data.(*common.Metric_StringData).StringData, ShouldEqual, "data")
	})

	getMetricTypesArg := &rpc.GetMetricTypesArg{}
	getMetricTypes, err := client.GetMetricTypes(context.Background(), getMetricTypesArg)
	Convey("calls GetMetricTypes", t, func() {
		So(err, ShouldBeNil)
		So(getMetricTypes, ShouldNotBeNil)
		So(len(getMetricTypes.Metrics), ShouldEqual, 2)
	})

}

func startCollectorServer() (int, Session, error) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}

	mockPlugin := &mockPlugin{}
	mockSessionState := &MockSessionState{
		Encoder:    encoding.NewGobEncoder(),
		listenPort: "0",
		token:      "abcdef",
		logger: log.New(os.Stdout,
			"test: ",
			log.Ldate|log.Ltime|log.Lshortfile),
		PingTimeoutDuration: time.Millisecond * 100,
		killChan:            make(chan int),
	}

	opts := []grpc.ServerOption{}
	grpcServer := grpc.NewServer(opts...)
	collectProxy := &gRPCCollectorProxy{
		Plugin:  mockPlugin,
		Session: mockSessionState,
		gRPCPluginProxy: gRPCPluginProxy{
			plugin:  mockPlugin,
			session: mockSessionState,
		},
	}

	rpc.RegisterCollectorServer(grpcServer, collectProxy)
	go func() {
		err := grpcServer.Serve(lis)
		if err != nil {
			log.Print(err)
		}
	}()
	return lis.Addr().(*net.TCPAddr).Port, mockSessionState, nil
}
