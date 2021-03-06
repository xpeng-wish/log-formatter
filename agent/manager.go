package agent

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/VertexC/log-formatter/agent/config"
	"github.com/VertexC/log-formatter/agent/connector"
	"github.com/VertexC/log-formatter/agent/input"
	"github.com/VertexC/log-formatter/agent/output"
	"github.com/VertexC/log-formatter/agent/pipeline"
	"github.com/VertexC/log-formatter/logger"
	agentpb "github.com/VertexC/log-formatter/proto/pkg/agent"
	ctrpb "github.com/VertexC/log-formatter/proto/pkg/controller"
	"github.com/VertexC/log-formatter/util"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"
)

type AgentsManagerConfig struct {
	BaseConfig config.ConfigBase
}

type AgentsManager struct {
	agentpb.UnimplementedLogFormatterAgentServer
	config *AgentsManagerConfig
	Status agentpb.Status
	agents map[string]Agent
	logger *logger.Logger
	// monitor rpc address
	monitorAddr string
	rpcPort     string
}

func NewAgentsManager(monitorAddr string, rpcPort string) (*AgentsManager, error) {
	logger := logger.NewLogger("AgentsManager")

	conn, err := connector.NewConnector()
	if err != nil {
		errMsg := fmt.Sprintf("Failed to create connector: %s\n", err)
		logger.Error.Println(errMsg)
		return nil, fmt.Errorf("%s", errMsg)
	}

	manager := &AgentsManager{
		logger:      logger,
		Status:      agentpb.Status_Stop,
		monitorAddr: monitorAddr,
		rpcPort:     rpcPort,
		config: &AgentsManagerConfig{
			BaseConfig: config.ConfigBase{
				MandantoryFields: []string{Input, Output, Pipeline},
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
	manager.Status = agentpb.Status_Running
	for _, agent := range manager.agents {
		go agent.Run()
	}
	go manager.StartRpcService()
	go manager.StartHearBeat()
}

func (manager *AgentsManager) Stop() {
	manager.logger.Info.Printf("Try to stop all agents")
	// the order has to be input->pipeline->output
	manager.agents[Input].Stop()
	manager.agents[Pipeline].Stop()
	manager.agents[Output].Stop()
	manager.logger.Info.Printf("All agents stopped")
}

func (manager *AgentsManager) SetConfig(content interface{}) error {
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
		if err := agent.SetConfig(manager.config.BaseConfig.Content[name]); err != nil {
			err = fmt.Errorf("Failed to create %s: %s", name, err)
			manager.logger.Error.Printf("%s\n", err)
			return err
		}
	}
	return nil
}

func (manager *AgentsManager) UpdateConfig(context context.Context, request *agentpb.UpdateConfigRequest) (*agentpb.UpdateConfigResponse, error) {
	configBytes := request.Config
	configStr := string(request.Config)
	manager.logger.Info.Printf("Get UpdateConfig Request: %s", configStr)
	configMapStr, err := config.LoadMapStrFromYamlBytes(configBytes)

	failedRes := &agentpb.UpdateConfigResponse{
		Header: &agentpb.ResponseHeader{
			Error: &agentpb.Error{
				Type: agentpb.ErrorType_FAILED,
			},
		},
	}
	if err != nil {
		errStr := fmt.Sprintf("Invalid Config :%s", err)
		manager.logger.Error.Printf("%s\n", errStr)
		failedRes.Header.Error.Message = errStr
		return failedRes, nil
	}
	// only support update pipeline config
	if pipelineCfg, ok := configMapStr[Pipeline]; !ok {
		errStr := fmt.Sprintf("Pipeline config not found\n")
		manager.logger.Error.Printf("%s\n", errStr)
		failedRes.Header.Error.Message = errStr
		return failedRes, nil
	} else {
		if err := manager.agents[Pipeline].(*pipeline.PipelineAgent).ChangeConfig(pipelineCfg); err != nil {
			errStr := fmt.Sprintf("Failed to change pipeline;s config\n")
			manager.logger.Error.Printf("%s\n", errStr)
			failedRes.Header.Error.Message = errStr
			return failedRes, nil
		}
	}
	manager.config.BaseConfig.Content[Pipeline] = configMapStr[Pipeline]
	return &agentpb.UpdateConfigResponse{
		Header: &agentpb.ResponseHeader{
			Error: &agentpb.Error{
				Type: agentpb.ErrorType_OK,
			},
		},
		Heartbeat: manager.prepareHeartBeat(),
	}, nil
}

func (manager *AgentsManager) StartHearBeat() {
	// Set up a connection to the server.
	if manager.monitorAddr == "" {
		manager.logger.Info.Printf("Start agent without heartbeat.\n")
		return
	}
	manager.logger.Info.Printf("Start agent with hearbeat to %s\n", manager.monitorAddr)
	var (
		conn *grpc.ClientConn
		err  error
	)
	for {
		conn, err = grpc.Dial(manager.monitorAddr, grpc.WithInsecure(), grpc.WithBlock())

		if err != nil {
			manager.logger.Error.Printf("Can not connect: %v", err)
		} else {

			defer conn.Close()
			manager.logger.Info.Printf("Start to Send Heartbeat\n")
			for {
				heartbeat := manager.prepareHeartBeat()
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
				time.Sleep(5 * time.Second)
			}
		}
		time.Sleep(5 * time.Second)
	}
}

func (manager *AgentsManager) GetHeartBeat(context context.Context, request *agentpb.HeartBeatRequest) (*agentpb.HeartBeat, error) {
	manager.logger.Debug.Println("Got Heart Beat Get Request")
	heartbeat := manager.prepareHeartBeat()
	return heartbeat, nil
}

func (manager *AgentsManager) prepareHeartBeat() *agentpb.HeartBeat {
	// ignore error here, as we always get cfg from bytes
	cfgBytes, _ := yaml.Marshal(manager.config.BaseConfig.Content)
	heartbeat := &agentpb.HeartBeat{
		Status:  manager.Status,
		RpcPort: manager.rpcPort,
		Config:  cfgBytes,
	}
	return heartbeat
}

func (manager *AgentsManager) StartRpcService() {
	port := manager.rpcPort
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
