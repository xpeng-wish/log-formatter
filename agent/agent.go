package agent

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/VertexC/log-formatter/agent/input"
	"github.com/VertexC/log-formatter/agent/output"
	"github.com/VertexC/log-formatter/agent/pipeline"
	"github.com/VertexC/log-formatter/config"
	"github.com/VertexC/log-formatter/connector"
	agentpb "github.com/VertexC/log-formatter/proto/pkg/agent"
	ctrpb "github.com/VertexC/log-formatter/proto/pkg/controller"
	"github.com/VertexC/log-formatter/util"

	"google.golang.org/grpc"
)

const (
	Input    = "input"
	Output   = "output"
	Pipeline = "pipeline"
)

type Agent interface {
	Run()
	SetConnector(*connector.Connector)
	ChangeConfig(interface{}) error
}

type AgentsManagerConfig struct {
	BaseConfig config.ConfigBase
	Id         uint64 `yaml: "id"`
	// address of controller
	Controller string `yaml: "controller"`
	// rpc port
	RpcPort string `yaml: "rpcport"`
}

type AgentsManager struct {
	agentpb.UnimplementedLogFormatterAgentServer
	config *AgentsManagerConfig
	Status agentpb.Status
	agents map[string]Agent
	logger *util.Logger
}

func NewAgentsManager() (*AgentsManager, error) {
	logger := util.NewLogger("AgentsManager")

	conn, err := connector.NewConnector()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create connector: %s\n", err)
		logger.Error.Println(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	manager := &AgentsManager{
		logger: logger,
		Status: agentpb.Status_Stop,
		config: &AgentsManagerConfig{
			BaseConfig: config.ConfigBase{
				MandantoryFields: []string{Input, Output, Pipeline, "id", "controller", "rpcport"},
			},
		},
		agents: map[string]Agent{
			Input:    new(input.InputAgent),
			Output:   new(output.OutputAgent),
			Pipeline: new(pipeline.PipelineAgent),
		},
	}

	for _, agent := range manager.agents {
		agent.SetConnector(conn)
	}

	return manager, nil
}

func (manager *AgentsManager) Run() {
	go manager.StartRpcService()
	for _, agent := range manager.agents {
		go agent.Run()
	}
	go manager.StartHearBeat()
}

func (manager *AgentsManager) ChangeConfig(content interface{}) error {
	// TODO: stop all agents

	contentMapStr, ok := content.(map[string]interface{})
	if !ok {
		return fmt.Errorf("Cannot convert given config to mapStr")
	}

	manager.config.BaseConfig.Content = contentMapStr
	if err := manager.config.BaseConfig.Validate(); err != nil {
		err = fmt.Errorf("Config validation failed: %s\n", err)
		manager.logger.Error.Printf("%s\n", err)
		return err
	}

	if err := util.YamlConvert(contentMapStr, manager.config); err != nil {
		err = fmt.Errorf("Failed to convert from yaml: %s\n", err)
		manager.logger.Error.Printf("%s\n", err)
		return nil
	}

	manager.logger.Info.Printf("Agents Manager has config :\n%+v\n", manager.config)

	// update config of each agent
	for name, agent := range manager.agents {
		if err := agent.ChangeConfig(manager.config.BaseConfig.Content[name]); err != nil {
			err = fmt.Errorf("Failed to create %s: %s", name, err)
			manager.logger.Error.Printf("%s\n", err)
			return err
		}
	}

	return nil
}

func (manager *AgentsManager) UpdateConfig(context context.Context, request *agentpb.UpdateConfigRequest) (*agentpb.UpdateConfigResponse, error) {
	configBytes := request.Config
	manager.logger.Info.Printf("Get UpdateConfig Request: %s", string(configBytes))
	return nil, nil
}

func (manager *AgentsManager) StartHearBeat() {
	// TODO: make sleep time internal configurable
	// Set up a connection to the server.
	var (
		conn *grpc.ClientConn
		err  error
	)

	for {
		conn, err = grpc.Dial("localhost:8081", grpc.WithInsecure(), grpc.WithBlock())

		if err != nil {
			manager.logger.Error.Fatalf("Can not connect: %v", err)

		} else {
			break
		}
		time.Sleep(5 * time.Second)
	}

	defer conn.Close()
	manager.logger.Info.Printf("Start to Send Heartbeat\n")
	for {
		heartbeat := &agentpb.HeartBeat{
			Status: manager.Status,
			Id:     manager.config.Id,
		}
		c := ctrpb.NewControllerClient(conn)

		// Contact the server and print out its response.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err != nil {
			manager.logger.Error.Fatalf("could not greet: %v", err)
		}

		r, err := c.UpdateAgentStatusRequest(ctx, heartbeat)
		if err != nil {
			manager.logger.Error.Printf("Failed to get response: %s\n", err)
		} else {
			manager.logger.Info.Printf("Got Response: %+v\n", *r)
		}
		time.Sleep(1 * time.Second)
	}
}

func (manager *AgentsManager) GetHeartBeat(context context.Context, request *agentpb.HeartBeatRequest) (*agentpb.HeartBeat, error) {
	manager.logger.Debug.Println("Got Heart Beat Get Request")
	msg := &agentpb.HeartBeat{
		Status: manager.Status,
		Id:     manager.config.Id,
	}
	return msg, nil
}

func (manager *AgentsManager) StartRpcService() {
	port := manager.config.RpcPort
	list, err := net.Listen("tcp", ":"+port)

	if err != nil {
		manager.logger.Error.Fatalf("Failed to listen %s: %s\n", port, err)
	}
	s := grpc.NewServer()
	agentpb.RegisterLogFormatterAgentServer(s, manager)
	manager.logger.Info.Printf("Start rpc listen: %s\n", port)
	go func() {
		if err := s.Serve(list); err != nil {
			manager.logger.Error.Fatalf("Failed to serve: %s\n", err)
		}
	}()
}
