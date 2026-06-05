package main

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var hostname string
var model string
var tokenTimeOut time.Duration = 5 * time.Second

// Home Assistant device information
type Device struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
}

// Home Assistant MQTT Discovery config for sensors
type SensorConfig struct {
	Name              string `json:"name"`
	StateTopic        string `json:"state_topic"`
	UniqueID          string `json:"unique_id"`
	UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
	DeviceClass       string `json:"device_class,omitempty"`
	ValueTemplate     string `json:"value_template,omitempty"`
	Device            Device `json:"device"`
}

// Home Assistant MQTT Discovery config for binary sensors
type BinarySensorConfig struct {
	Name         string `json:"name"`
	StateTopic   string `json:"state_topic"`
	UniqueID     string `json:"unique_id"`
	DeviceClass  string `json:"device_class,omitempty"`
	PayloadOn    string `json:"payload_on,omitempty"`
	PayloadOff   string `json:"payload_off,omitempty"`
	Device       Device `json:"device"`
}

type ButtonConfig struct {
	Name              string `json:"name"`
	CommandTopic      string `json:"command_topic"`
	PayloadPress	  string `json:"payload_press,omitempty"`
	UniqueID          string `json:"unique_id"`
	Device            Device `json:"device"`
}

// Home Assistant MQTT Discovery config for number entities (volume control)
type NumberConfig struct {
	Name         string  `json:"name"`
	CommandTopic string  `json:"command_topic"`
	StateTopic   string  `json:"state_topic"`
	UniqueID     string  `json:"unique_id"`
	Min          float32 `json:"min,omitempty"` // 改为 float32 以支持 0.0
	Max          float32 `json:"max,omitempty"` // 改为 float32 以支持 1.0
	Step         float32 `json:"step,omitempty"`
	Device       Device  `json:"device"`
}

// Home Assistant MQTT Discovery config for light entities
type LightConfig struct {
	Name         string  `json:"name"`
	CommandTopic string  `json:"command_topic"`
	StateTopic   string  `json:"state_topic"`
	UniqueID     string  `json:"unique_id"`
	Device       Device  `json:"device"`
	BrightnessStateTopic string `json:"brightness_state_topic,omitempty"`
	BrightnessCommandTopic string `json:"brightness_command_topic,omitempty"`
}

type config struct {
	Ip       string `yaml:"mqtt_ip"`
	Port     string `yaml:"mqtt_port"`
	User     string `yaml:"mqtt_user"`
	Password string `yaml:"mqtt_password"`
	BrightnessBinaryPath string `yaml:"brightness_binary_path"` // 新增亮度控制路径配置
}

func (c *config) getConfig() *config {

	configContent, err := ioutil.ReadFile("mac2mqtt.yaml")
	if err != nil {
		log.Fatal(err)
	}

	err = yaml.Unmarshal(configContent, c)
	if err != nil {
		log.Fatal(err)
	}

	if c.Ip == "" {
		log.Fatal("Must specify mqtt_ip in mac2mqtt.yaml")
	}

	if c.Port == "" {
		log.Fatal("Must specify mqtt_port in mac2mqtt.yaml")
	}

	if c.User == "" {
		log.Fatal("Must specify mqtt_user in mac2mqtt.yaml")
	}

	if c.Password == "" {
		log.Fatal("Must specify mqtt_password in mac2mqtt.yaml")
	}

	// 检查亮度控制路径是否存在
	if c.BrightnessBinaryPath == "" {
		log.Fatal("Must specify brightness_binary_path in mac2mqtt.yaml")
	}

	return c
}

func getHostname() string {

	hostname, err := os.Hostname()

	if err != nil {
		log.Fatal(err)
	}

	// "name.local" => "name"
	firstPart := strings.Split(hostname, ".")[0]

	// remove all symbols, but [a-zA-Z0-9_-]
	reg, err := regexp.Compile("[^a-zA-Z0-9_-]+")
	if err != nil {
		log.Fatal(err)
	}
	firstPart = reg.ReplaceAllString(firstPart, "")

	return firstPart
}

func getCommandOutput(name string, arg ...string) string {
	cmd := exec.Command(name, arg...)

	stdout, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}

	stdoutStr := string(stdout)
	stdoutStr = strings.TrimSuffix(stdoutStr, "\n")

	return stdoutStr
}

func getMuteStatus() bool {
	output := getCommandOutput("/usr/bin/osascript", "-e", "output muted of (get volume settings)")

	b, err := strconv.ParseBool(output)
	if err != nil {
		log.Fatal(err)
	}

	return b
}

func getCurrentVolume() int {
	output := getCommandOutput("/usr/bin/osascript", "-e", "output volume of (get volume settings)")

	i, err := strconv.Atoi(output)
	if err != nil {
		log.Fatal(err)
	}

	return i
}

// 获取当前亮度值
func getCurrentBrightness(config *config) float32 {
	output := getCommandOutput(config.BrightnessBinaryPath, "-lv")

	// 解析输出，查找亮度值
	re := regexp.MustCompile(`brightness\s+(\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	
	if len(matches) < 2 {
		log.Printf("无法从亮度命令输出中提取亮度值: %s", output)
		return 0.0
	}
	
	brightnessStr := matches[1]
	brightness, err := strconv.ParseFloat(brightnessStr, 32)
	if err != nil {
		log.Printf("解析亮度值失败: %s, 错误: %v", brightnessStr, err)
		return 0.0
	}

	return float32(brightness)
}

// 设置亮度值 (0.0 到 1.0)
func setBrightness(config *config, brightness float32) {
	// 格式化浮点数，保留两位小数，防止出现 0.3333333 这种长串
	brightnessStr := strconv.FormatFloat(float64(brightness), 'f', 2, 32)
	
	// 使用二进制工具设置亮度
	runCommand(config.BrightnessBinaryPath, brightnessStr)
	log.Printf("执行亮度设置命令: %s", brightnessStr)
}

func runCommand(name string, arg ...string) {
	cmd := exec.Command(name, arg...)

	_, err := cmd.Output()
	if err != nil {
		log.Fatal(err)
	}
}

// Combined function to get both battery percentage and charging status
func getBatteryInfo() (percent string, isCharging bool) {
	output := getCommandOutput("/usr/bin/pmset", "-g", "batt")

	// $ /usr/bin/pmset -g batt
	// Now drawing from 'Battery Power'
	//  -InternalBattery-0 (id=4653155)        100%; discharging; 20:00 remaining present: true
	
	// Extract battery percentage
	r := regexp.MustCompile(`(\d+)%`)
	percent = r.FindStringSubmatch(output)[1]
	
	// Check if drawing power from AC Power source
	isCharging = strings.Contains(output, "AC Power")
	
	return percent, isCharging
}

// from 0 to 100
func setVolume(i int) {
	runCommand("/usr/bin/osascript", "-e", "set volume output volume "+strconv.Itoa(i))
}

// true - turn mute on
// false - turn mute off
func setMute(b bool) {
	runCommand("/usr/bin/osascript", "-e", "set volume output muted "+strconv.FormatBool(b))
}

func commandSleep() {
	runCommand("pmset", "sleepnow")
}

func commandDisplaySleep() {
	runCommand("pmset", "displaysleepnow")
}

func commandShutdown() {

	if os.Getuid() == 0 {
		// if the program is run by root user we are doing the most powerfull shutdown - that always shuts down the computer
		runCommand("shutdown", "-h", "now")
	} else {
		// if the program is run by ordinary user we are trying to shutdown, but it may fail if the other user is logged in
		runCommand("/usr/bin/osascript", "-e", "tell app \"System Events\" to shut down")
	}
}

var messagePubHandler mqtt.MessageHandler = func(client mqtt.Client, msg mqtt.Message) {
	log.Printf("Received message: %s from topic: %s\n", msg.Payload(), msg.Topic())
}

var connectHandler mqtt.OnConnectHandler = func(client mqtt.Client) {
	log.Println("Connected to MQTT")

	publishHADiscoveryConfig(client)

	listen(client, getTopicPrefix()+"/command/#")
}

var connectLostHandler mqtt.ConnectionLostHandler = func(client mqtt.Client, err error) {
	log.Printf("Disconnected from MQTT: %v", err)
	
	// Attempt to reconnect
	go func() {
		for {
			log.Println("Attempting to reconnect to MQTT...")
			token := client.Connect()
			if token.WaitTimeout(5*time.Second) && token.Error() == nil {
				log.Println("Reconnected to MQTT successfully")
				break
			} else {
				log.Printf("Failed to reconnect: %v. Retrying in 5 seconds...", token.Error())
				time.Sleep(5 * time.Second)
			}
		}
	}()
}

var client mqtt.Client
var globalConfig *config // 全局配置变量

func getMQTTClient(ip, port, user, password string) mqtt.Client {

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%s", ip, port))
	opts.SetUsername(user)
	opts.SetPassword(password)
	opts.SetClientID("mac2mqtt") 
	opts.SetAutoReconnect(true)           // Enable auto-reconnect
	opts.SetConnectRetry(true)            // Enable connect retry
	opts.SetConnectRetryInterval(5 * time.Second) // Set retry interval

	opts.OnConnect = connectHandler
	opts.OnConnectionLost = connectLostHandler

	client = mqtt.NewClient(opts)
	token := client.Connect();
	if !token.WaitTimeout(5*time.Second) {
		log.Printf("MQTT connection timed out")
		panic("MQTT connection timed out")
	} else if token.Error() != nil {
		log.Printf("MQTT connection error: %v", token.Error())
		panic(token.Error())
	}

	return client
}

func getTopicPrefix() string {
	return "homeassistant/" + hostname
}

func listen(client mqtt.Client, topic string) {

	token := client.Subscribe(topic, 0, func(client mqtt.Client, msg mqtt.Message) {

		topicPrefix := getTopicPrefix()

		topic := string(msg.Topic())
		commd := string(msg.Payload())

		log.Printf("Received command:  [ %s ] [ %s ]", topic, commd)

		if topic == topicPrefix+"/command/volume" {

			i, err := strconv.Atoi(commd)
			if err == nil && i >= 0 && i <= 100 {

				setVolume(i)

				time.Sleep(1 * time.Second)

				updateVolume(client)
				updateMute(client)

			} else {
				log.Println("Incorrect volume value")
			}

		} else if topic == topicPrefix+"/command/mute" {

			b, err := strconv.ParseBool(commd)
			if err == nil {
				setMute(b)

				time.Sleep(1 * time.Second)

				updateVolume(client)
				updateMute(client)

			} else {
				log.Println("Incorrect mute value")
			}

		} else if topic == topicPrefix+"/command/sleep" {

			if string(msg.Payload()) == "sleep" {
				
				commandSleep()
			}

		} else if topic == topicPrefix+"/command/displaysleep" {

			if string(msg.Payload()) == "displaysleep" {
				
				commandDisplaySleep()
			}

		} else if topic == topicPrefix+"/command/shutdown" {

			if string(msg.Payload()) == "shutdown" {
				
				commandShutdown()
			}

		} else if topic == topicPrefix+"/command/brightness" { // 处理亮度命令
			// 1. 接收 MQTT 发来的字符串（假设是 "50"）
			rawValue := string(msg.Payload())
			
			// 2. 转换为整数
			intValue, err := strconv.Atoi(rawValue)
			if err != nil {
				log.Println("亮度值格式错误，无法转换为数字:", rawValue)
				return
			}

			// 3. 边界检查 (0-100)
			if intValue < 0 {
				intValue = 0
			} else if intValue > 100 {
				intValue = 100
			}

			// 4. 关键转换：将 0-100 的整数映射为 0.0-1.0 的浮点数
			floatVal := float32(intValue) / 100.0

			// 5. 调用设置函数
			setBrightness(globalConfig, floatVal)
			
			// 6. 立即反馈状态（可选，防止轮询延迟）
			time.Sleep(500 * time.Millisecond)
			updateBrightness(client)
		}

	})

	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Subscribe timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Token error: %v", token.Error())
	}
}

func updateVolume(client mqtt.Client) {
	token := client.Publish(getTopicPrefix()+"/state/volume", 0, false, strconv.Itoa(getCurrentVolume()))
	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Update volume timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Error updating volume: %v", token.Error())
	}
}

func updateMute(client mqtt.Client) {
	token := client.Publish(getTopicPrefix()+"/state/mute", 0, false, strconv.FormatBool(getMuteStatus()))
	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Update mute timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Error updating mute: %v", token.Error())
	}
}

// 更新亮度状态
func updateBrightness(client mqtt.Client) {
	brightness := getCurrentBrightness(globalConfig)
	
	// 将浮点亮度值转换为 0-100 的整数用于 MQTT 上报（与 HA 配置保持一致）
	brightnessInt := int(brightness * 100)
	
	token := client.Publish(getTopicPrefix()+"/state/brightness", 0, false, strconv.Itoa(brightnessInt))
	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Update brightness timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Error updating brightness: %v", token.Error())
	}
}

func updateBattery(client mqtt.Client) {
	percent, isCharging := getBatteryInfo()
	token := client.Publish(getTopicPrefix()+"/state/battery", 0, false, percent)
	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Update battery timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Error updating battery: %v", token.Error())
	}
	
	// Also publish charging status
	token = client.Publish(getTopicPrefix()+"/state/power_adapter", 0, false, strconv.FormatBool(isCharging))
	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Update power adapter timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Error updating power adapter: %v", token.Error())
	}
}


func publishHADiscoveryConfig(client mqtt.Client) {
	topicPrefix := getTopicPrefix()
	
	device := Device{
		Identifiers:  []string{hostname},
		Name:         hostname,
		Manufacturer: "Apple",
		Model:        model,
	}

	// Battery sensor
	batteryConfig := SensorConfig{
		Name:              hostname + " Battery Level",
		StateTopic:        topicPrefix + "/state/battery",
		UniqueID:          hostname + "_battery",
		UnitOfMeasurement: "%",
		DeviceClass:       "battery",
		Device:            device,
	}
	publishConfig(client, "sensor", hostname+"_battery", batteryConfig)

	// Power adapter binary sensor
	powerAdapterConfig := BinarySensorConfig{
		Name:        hostname + " Power Adapter",
		StateTopic:  topicPrefix + "/state/power_adapter",
  		PayloadOn:   "true",
  		PayloadOff:  "false",
		UniqueID:    hostname + "_power_adapter",
		DeviceClass: "plug",
		Device:      device,
	}
	publishConfig(client, "binary_sensor", hostname+"_power_adapter", powerAdapterConfig)

	// Volume control (number entity) - includes state feedback
	volumeNumberConfig := NumberConfig{
		Name:         hostname + " Volume",
		CommandTopic: topicPrefix + "/command/volume",
		StateTopic:   topicPrefix + "/state/volume",
		UniqueID:     hostname + "_volume",
		Min:          0,
		Max:          100,
		Device:       device,
	}
	publishConfig(client, "number", hostname+"_volume", volumeNumberConfig)

	// Brightness control (number entity) - includes state feedback
	brightnessNumberConfig := NumberConfig{
		Name:         hostname + " Brightness",
		CommandTopic: topicPrefix + "/command/brightness",
		StateTopic:   topicPrefix + "/state/brightness",
		UniqueID:     hostname + "_brightness",
		Min:          0,
		Max:          100,
		Device:       device,
	}
	publishConfig(client, "number", hostname+"_brightness", brightnessNumberConfig)

	// Mute Button with state feedback
	muteButtonConfig := ButtonConfig{
		Name:         hostname + " Mute",
		CommandTopic: topicPrefix + "/command/mute",
		PayloadPress: "true",
		UniqueID:     hostname + "_mute",
		Device:       device,
	}
	publishConfig(client, "button", hostname+"_mute", muteButtonConfig)

	// Sleep command Button with state feedback
	sleepButtonConfig := ButtonConfig{
		Name:         hostname + " Sleep",
		CommandTopic: topicPrefix + "/command/sleep",
  		PayloadPress: "sleep",
		UniqueID:     hostname + "_sleep",
		Device:       device,
	}
	publishConfig(client, "button", hostname+"_sleep", sleepButtonConfig)

	// Display sleep command Button with state feedback
	displaySleepButtonConfig := ButtonConfig{
		Name:         hostname + " Display Sleep",
		CommandTopic: topicPrefix + "/command/displaysleep",
  		PayloadPress: "displaysleep",
		UniqueID:     hostname + "_display_sleep",
		Device:       device,
	}
	publishConfig(client, "button", hostname+"_display_sleep", displaySleepButtonConfig)

	// Shutdown command Button with state feedback
	shutdownButtonConfig := ButtonConfig{
		Name:         hostname + " Shutdown",
		CommandTopic: topicPrefix + "/command/shutdown",
  		PayloadPress: "shutdown",
		UniqueID:     hostname + "_shutdown",
		Device:       device,
	}
	publishConfig(client, "button", hostname+"_shutdown", shutdownButtonConfig)

	
}

func publishConfig(client mqtt.Client, component string, objectId string, config interface{}) {
	configTopic := fmt.Sprintf("homeassistant/%s/%s/config", component, objectId)
	configBytes, err := json.Marshal(config)
	if err != nil {
		log.Printf("Error marshaling config: %v", err)
		return
	}

	token := client.Publish(configTopic, 0, true, configBytes)
	if !token.WaitTimeout(tokenTimeOut) {
		log.Printf("Publish config timed out after %v", tokenTimeOut)
	} else if token.Error() != nil {
		log.Printf("Error publishing config: %v", token.Error())
	} else {
		log.Printf("Published %s config to %s", component, configTopic)
	}
}

func main() {

	log.Println("Started")

	var c config
	c.getConfig()

	var wg sync.WaitGroup

	hostname = "MacBookPRO_M2"

	model = hostname

	globalConfig = &c // 设置全局配置变量

	mqttClient := getMQTTClient(c.Ip, c.Port, c.User, c.Password)

	volumeTicker := time.NewTicker(2 * time.Second)
	batteryTicker := time.NewTicker(60 * time.Second)

	wg.Add(1)
	go func() {
		for {
			select {
			case _ = <-volumeTicker.C:
				updateVolume(mqttClient)
				updateMute(mqttClient)
				updateBrightness(mqttClient)


			case _ = <-batteryTicker.C:
				updateBattery(mqttClient)
				// Power adapter status is now published together with battery info
			}
		}
	}()

	wg.Wait()

}
