package main

import (
	"crane/bolt"
	"crane/core/messages"
	"crane/core/utils"
	"crane/spout"
	"crane/topology"
	"fmt"
	"hash/fnv"
	"log"
	"net"
	"sort"
	"sync"
	"time"
)

// Driver, the master node daemon server for scheduling and
// dispaching the spouts or bolts task
type Driver struct {
	Pub                   *messages.Publisher
	Topo                  *topology.Topology
	SupervisorIdMap       []string
	LockSIM               sync.RWMutex
	TopologyGraph         map[string][]interface{}
	SpoutMap              map[string]spout.SpoutInst
	BoltMap               map[string]bolt.BoltInst
	VmIndexMap            map[int]string
	CtlTimer              []*time.Timer
	SuspendResponseCount  int
	SnapshotResponseCount int
	TaskSum               int
	SnapshotVersion       int
	SnapshotInterval      int
}

// Factory mode to return the Driver instance
func NewDriver(addr string) *Driver {
	driver := &Driver{}
	driver.Pub = messages.NewPublisher(addr)
	driver.SupervisorIdMap = make([]string, 0)
	driver.VmIndexMap = make(map[int]string)
	driver.CtlTimer = make([]*time.Timer, 0)
	driver.SuspendResponseCount = 0
	driver.SnapshotResponseCount = 0
	driver.SnapshotVersion = 0
	driver.SnapshotInterval = 30
	return driver
}

// Start the Driver daemon
func (d *Driver) StartDaemon() {
	go d.Pub.AcceptConns()
	go d.Pub.PublishMessage(d.Pub.PublishBoard)
	for {
		for connId, channel := range d.Pub.Channels {
			d.Pub.RWLock.RLock()
			select {
			case supervisorMsg := <-channel:
				payload := utils.CheckType(supervisorMsg.Payload)
				log.Printf("Receiving %s request form %s\n", payload.Header.Type, connId)
				// parse the header information
				switch payload.Header.Type {
				// if it is the join request from supervisor
				case utils.JOIN_REQUEST:
					content := &utils.JoinRequest{}
					utils.Unmarshal(payload.Content, content)
					d.LockSIM.Lock()
					d.SupervisorIdMap = append(d.SupervisorIdMap, connId)
					d.LockSIM.Unlock()
					log.Println("Supervisor ID Name", content.Name)
				// if it is the connection notification about the connection pools
				case utils.CONN_NOTIFY:
					content := &messages.ConnNotify{}
					utils.Unmarshal(payload.Content, content)
					if content.Type == messages.CONN_DELETE {
						for _, timer := range d.CtlTimer {
							log.Println("Clean previous timer")
							timer.Stop()
						}
						d.CtlTimer = make([]*time.Timer, 0)

						d.LockSIM.RLock()
						for index, connId_ := range d.SupervisorIdMap {
							if connId_ == connId {
								d.SupervisorIdMap = append(d.SupervisorIdMap[:index], d.SupervisorIdMap[index+1:]...)
								delete(d.Pub.Channels, connId)
								go d.RestoreRequest()
							}
						}
						d.LockSIM.RUnlock()
					}
				// if it is the topology submitted from the client, which is
				// the application written by the developer
				case utils.TOPO_SUBMISSION:
					d.Pub.PublishBoard <- messages.Message{
						Payload:      []byte("OK"),
						TargetConnId: connId,
					}
					topo := &topology.Topology{}
					utils.Unmarshal(payload.Content, topo)
					d.Topo = topo
					d.BuildTopology(topo)
				// Spout instance responses
				case utils.SUSPEND_RESPONSE:
					d.SuspendResponseCount++

					if d.SuspendResponseCount == len(d.SpoutMap) {
						d.Snapshot()
						d.SuspendResponseCount = 0
					}
				// Snapshot completion responses from all supervisors
				case utils.SNAPSHOT_RESPONSE:
					d.SnapshotResponseCount++

					if d.SnapshotResponseCount == len(d.SupervisorIdMap) {
						// Confirm a correct version snapshot has completed
						d.SnapshotVersion++
						d.SnapshotResponseCount = 0
					}
				}
			default:
			}
			d.Pub.RWLock.RUnlock()
		}
	}
}

// Build the graph topology using vector-edge map
func (d *Driver) BuildTopology(topo *topology.Topology) {
	// make the map for a task name (spout or bolt) to the task instance
	d.TopologyGraph = make(map[string][]interface{})
	d.SpoutMap = make(map[string]spout.SpoutInst)
	d.BoltMap = make(map[string]bolt.BoltInst)
	if len(d.SupervisorIdMap) == 0 {
		return
	}

	// build the vectors table
	for i, _ := range topo.Bolts {
		preVecs := topo.Bolts[i].PrevTaskNames
		for _, vec := range preVecs {
			if d.TopologyGraph[vec] == nil {
				d.TopologyGraph[vec] = make([]interface{}, 0)
			}
			d.TopologyGraph[vec] = append(d.TopologyGraph[vec], &topo.Bolts[i])
		}
		topo.Bolts[i].TaskAddrs = make([]string, 0)
	}

	for i, _ := range topo.Spouts {
		preVec := "None"
		if d.TopologyGraph[preVec] == nil {
			d.TopologyGraph[preVec] = make([]interface{}, 0)
		}
		d.TopologyGraph[preVec] = append(d.TopologyGraph[preVec], &topo.Spouts[i])
		topo.Spouts[i].TaskAddrs = make([]string, 0)
	}

	visited := make(map[string]bool)
	count := 0
	addrs := make(map[int][]interface{})
	d.GenTopologyMessages("None", &visited, &count, &addrs)
	d.PrintTopology("None", 0)
	// Stage 1 : Send pull request to supervisor to pull the file needed
	// including the plugin file and state files for restoring
	if d.SnapshotVersion == 0 {
		for id, _ := range addrs {
			targetId := d.SupervisorIdMap[uint32(id)]
			msg := utils.FilePull{topo.Bolts[0].PluginFile}
			b, _ := utils.Marshal(utils.FILE_PULL, msg)
			d.Pub.PublishBoard <- messages.Message{
				Payload:      b,
				TargetConnId: targetId,
			}
		}
	}

	d.TaskSum = count

	// To store the keys in slice in sorted order
	var keys []int
	for k := range addrs {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	log.Println("Supervisors", addrs, keys)

	countMap := make(map[string]int)

	//spoutsSuccBoltsConnIdMap := make(map[string]map[string]map[string]int)
	// generate bolts connection ID to count num mapping
	/*for _, k := range keys {*/
	//tasks := addrs[k]
	//targetId := d.SupervisorIdMap[uint32(k)]
	//for _, task := range tasks {
	//bolt, ok := task.(*bolt.BoltInst)
	//if !ok {
	//continue
	//}
	//if countMap[bolt.Name] == 0 {
	//countMap[bolt.Name] = 1
	//}
	//for _, spoutName := range bolt.PrevTaskNames {
	//_, ok = d.SpoutMap[spoutName]
	//if ok {
	//if spoutsSuccBoltsConnIdMap[spoutName] == nil {
	//spoutsSuccBoltsConnIdMap[spoutName] = make(map[string]map[string]int)
	//spoutsSuccBoltsConnIdMap[spoutName][bolt.Name] = make(map[string]int)

	//}
	//spoutsSuccBoltsConnIdMap[spoutName][bolt.Name][targetId] = countMap[bolt.Name]
	//}
	//}
	//countMap[bolt.Name]++
	//}
	/*}*/

	if d.SnapshotVersion > 0 {
		for _, k := range keys {
			tasks := addrs[k]
			targetId := d.SupervisorIdMap[uint32(k)]
			for _, task := range tasks {
				time.Sleep(400 * time.Millisecond)
				spout, ok := task.(*spout.SpoutInst)
				if ok {
					if countMap[spout.Name] == 0 {
						countMap[spout.Name] = 1
					}
					stateFileName := spout.Name + "_" + fmt.Sprintf("%d_%d", countMap[spout.Name], d.SnapshotVersion-1)
					msg := utils.FilePull{stateFileName}
					b, _ := utils.Marshal(utils.FILE_PULL, msg)
					d.Pub.PublishBoard <- messages.Message{
						Payload:      b,
						TargetConnId: targetId,
					}
					countMap[spout.Name]++
				} else {
					bolt, _ := task.(*bolt.BoltInst)
					if countMap[bolt.Name] == 0 {
						countMap[bolt.Name] = 1
					}

					stateFileName := bolt.Name + "_" + fmt.Sprintf("%d_%d", countMap[bolt.Name], d.SnapshotVersion-1)
					msg := utils.FilePull{stateFileName}
					b, _ := utils.Marshal(utils.FILE_PULL, msg)
					d.Pub.PublishBoard <- messages.Message{
						Payload:      b,
						TargetConnId: targetId,
					}
					countMap[bolt.Name]++
				}
			}
		}
	}

	time.Sleep(5 * time.Second)
	// Stage 2 : Send the task message information to supervisors
	countMap = make(map[string]int)
	for _, id := range keys {
		tasks := addrs[id]
		targetId := d.SupervisorIdMap[uint32(id)]
		for offset, task := range tasks {
			time.Sleep(20 * time.Millisecond)
			spout, ok := task.(*spout.SpoutInst)
			if ok {
				if countMap[spout.Name] == 0 {
					countMap[spout.Name] = 1
				}
				msg := utils.SpoutTaskMessage{
					Name:            spout.Name + "_" + fmt.Sprintf("%d", countMap[spout.Name]),
					GroupingHint:    spout.GroupingHint,
					FieldIndex:      spout.FieldIndex,
					PluginFile:      spout.PluginFile,
					PluginSymbol:    spout.PluginSymbol,
					Port:            fmt.Sprintf("%d", utils.CONTRACTOR_BASE_PORT+offset),
					SnapshotVersion: d.SnapshotVersion - 1,
				}
				fmt.Println(msg)
				b, _ := utils.Marshal(utils.SPOUT_TASK, msg)
				d.Pub.PublishBoard <- messages.Message{
					Payload:      b,
					TargetConnId: targetId,
				}
				countMap[spout.Name]++
			} else {
				bolt, ok := task.(*bolt.BoltInst)
				if countMap[bolt.Name] == 0 {
					countMap[bolt.Name] = 1
				}
				msg := utils.BoltTaskMessage{
					Name:                 bolt.Name + "_" + fmt.Sprintf("%d", countMap[bolt.Name]),
					SuccBoltGroupingHint: bolt.GroupingHint,
					SuccBoltFieldIndex:   bolt.FieldIndex,
					PluginFile:           bolt.PluginFile,
					PluginSymbol:         bolt.PluginSymbol,
					Port:                 fmt.Sprintf("%d", utils.CONTRACTOR_BASE_PORT+offset),
					SnapshotVersion:      d.SnapshotVersion - 1,
				}

				_, ok = d.SpoutMap[bolt.PrevTaskNames[0]]
				if ok {
					prev := d.SpoutMap[bolt.PrevTaskNames[0]]
					msg.PrevBoltGroupingHint = prev.GroupingHint
					msg.PrevBoltFieldIndex = prev.FieldIndex
				} else {
					prev := d.BoltMap[bolt.PrevTaskNames[0]]
					msg.PrevBoltGroupingHint = prev.GroupingHint
					msg.PrevBoltFieldIndex = prev.FieldIndex
				}

				addr := make([]string, 0)
				for _, name := range bolt.PrevTaskNames {
					_, ok := d.SpoutMap[name]
					if ok {
						prev := d.SpoutMap[name]
						addr = append(addr, prev.TaskAddrs...)
					} else {
						prev := d.BoltMap[name]
						addr = append(addr, prev.TaskAddrs...)
					}
				}
				msg.PrevBoltAddr = addr
				fmt.Println(msg)

				b, _ := utils.Marshal(utils.BOLT_TASK, msg)
				d.Pub.PublishBoard <- messages.Message{
					Payload:      b,
					TargetConnId: targetId,
				}
				countMap[bolt.Name]++
			}
		}
	}

	time.Sleep(5 * time.Second)

	// Stage 3 : Send dispatch signal
	for id, _ := range addrs {
		d.LockSIM.RLock()
		targetId := d.SupervisorIdMap[uint32(id)]
		d.LockSIM.RUnlock()
		b, _ := utils.Marshal(utils.TASK_ALL_DISPATCHED, "ok")
		d.Pub.PublishBoard <- messages.Message{
			Payload:      b,
			TargetConnId: targetId,
		}
	}

	// Stage 4 : Start snapshot process
	go d.SuspendRequest()
}

// Failover for a supervisor down and start restore process
func (d *Driver) RestoreRequest() {
	// no topology submitted
	if d.Topo == nil {
		return
	}

	d.SnapshotInterval += 20

	// reset the counter of two snapshot state process responses
	d.SuspendResponseCount = 0
	d.SnapshotResponseCount = 0

	timer := time.NewTimer(2 * time.Second)
	d.CtlTimer = append(d.CtlTimer, timer)
	go func() {
		<-timer.C
		log.Println("Timeout, build topology")
		// send out restore message to all other supervisors
		// and let them shutdown current workers
		b, _ := utils.Marshal(utils.RESTORE_REQUEST, utils.RESTORE_REQUEST)
		for _, connId := range d.SupervisorIdMap {
			d.Pub.PublishBoard <- messages.Message{
				Payload:      b,
				TargetConnId: connId,
			}
		}
		time.Sleep(800 * time.Millisecond)
		d.BuildTopology(d.Topo)
	}()

}

// Timer to request suspend on spout instances
// before request backup snapshot for each node workers
func (d *Driver) SuspendRequest() {
	for {
		time.Sleep(50 * time.Second)
		hostConnIdMap := make(map[string]string)
		for _, connId := range d.SupervisorIdMap {
			host, _, _ := net.SplitHostPort(connId)
			hostConnIdMap[host] = connId
		}

		spoutHosts := make(map[string]string)
		for _, spout := range d.SpoutMap {
			for _, addr := range spout.TaskAddrs {
				host, _, _ := net.SplitHostPort(addr)
				spoutHosts[host] = hostConnIdMap[host]
			}
		}
		if d.SnapshotInterval > 30 {
			for i := d.SnapshotInterval; i >= 30; i-- {
				time.Sleep(time.Second)
			}
			d.SnapshotInterval = 30
		}

		for _, connId := range spoutHosts {
			b, _ := utils.Marshal(utils.SUSPEND_REQUEST, utils.SUSPEND_REQUEST)
			d.Pub.PublishBoard <- messages.Message{
				Payload:      b,
				TargetConnId: connId,
			}
		}
	}
}

// Send snapshot signal to supervisors
func (d *Driver) Snapshot() {
	if d.SnapshotVersion == 0 {
		d.SnapshotVersion = 1
	}
	for _, connId := range d.SupervisorIdMap {
		b, _ := utils.Marshal(utils.SNAPSHOT_REQUEST, d.SnapshotVersion)
		d.Pub.PublishBoard <- messages.Message{
			Payload:      b,
			TargetConnId: connId,
		}
	}

}

// Generate Topology Messages for each bolt or spout instance
func (d *Driver) GenTopologyMessages(next string, visited *map[string]bool, count *int, addrs *map[int][]interface{}) {
	if d.TopologyGraph == nil {
		log.Println("No topology has been built")
		return
	}

	startVecs := d.TopologyGraph[next]
	if startVecs == nil {
		return
	}

	for _, vec := range startVecs {
		if next == "None" {
			spout := vec.(*spout.SpoutInst)
			if (*visited)[(*spout).Name] == true {
				continue
			}

			fmt.Printf("#%s ", (*spout).Name)
			(*visited)[(*spout).Name] = true
			for i := 0; i < (*spout).InstNum; i++ {
				id := (*count) % len(d.SupervisorIdMap)
				if (*addrs)[id] == nil {
					(*addrs)[id] = make([]interface{}, 0)
				}
				targetId := d.SupervisorIdMap[uint32(id)]
				host, _, _ := net.SplitHostPort(targetId)
				(*spout).TaskAddrs = append((*spout).TaskAddrs, host+":"+fmt.Sprintf("%d", utils.CONTRACTOR_BASE_PORT+len((*addrs)[id])))
				(*addrs)[id] = append((*addrs)[id], spout)
				(*count)++
			}
			d.GenTopologyMessages(spout.Name, visited, count, addrs)
		} else {
			bolt := vec.(*bolt.BoltInst)
			if (*visited)[(*bolt).Name] == true {
				continue
			}
			(*visited)[(*bolt).Name] = true
			fmt.Printf("#%s ", (*bolt).Name)
			for i := 0; i < (*bolt).InstNum; i++ {
				id := (*count) % len(d.SupervisorIdMap)
				if (*addrs)[id] == nil {
					(*addrs)[id] = make([]interface{}, 0)
				}
				targetId := d.SupervisorIdMap[uint32(id)]
				host, _, _ := net.SplitHostPort(targetId)
				(*bolt).TaskAddrs = append((*bolt).TaskAddrs, host+":"+fmt.Sprintf("%d", utils.CONTRACTOR_BASE_PORT+len((*addrs)[id])))
				(*addrs)[id] = append((*addrs)[id], bolt)
				(*count)++
			}
			d.GenTopologyMessages(bolt.Name, visited, count, addrs)
		}
	}
}

// Output the topology in the std out
func (d *Driver) PrintTopology(next string, level int) {
	if d.TopologyGraph == nil {
		log.Println("No topology has been built")
		return
	}
	startVecs := d.TopologyGraph[next]
	if startVecs == nil {
		return
	}
	for _, vec := range startVecs {
		fmt.Printf("\n")
		for i := 0; i < level; i++ {
			fmt.Printf("  ")
		}
		if next == "None" {
			fmt.Printf("#%s ", vec.(*spout.SpoutInst).Name)
			d.SpoutMap[vec.(*spout.SpoutInst).Name] = (*vec.(*spout.SpoutInst))
			fmt.Println(vec.(*spout.SpoutInst).TaskAddrs)
			d.PrintTopology(vec.(*spout.SpoutInst).Name, level+1)
		} else {
			fmt.Printf("--- %s ", vec.(*bolt.BoltInst).Name)
			d.BoltMap[vec.(*bolt.BoltInst).Name] = (*vec.(*bolt.BoltInst))
			fmt.Println(vec.(*bolt.BoltInst).TaskAddrs)
			d.PrintTopology(vec.(*bolt.BoltInst).Name, level+1)
		}
	}
}

func (d *Driver) Hashcode(id string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(id))
	hashcode := h.Sum32()
	hashcode = (hashcode + 5) >> 5 % uint32(len(d.SupervisorIdMap))
	return hashcode
}

func main() {
	driver := NewDriver(":" + fmt.Sprintf("%d", utils.DRIVER_PORT))
	LocalIP := utils.GetLocalIP().String()
	LocalHostname := utils.GetLocalHostname()
	log.Printf("Local Machine Info [%s] [%s]\n", LocalIP, LocalHostname)

	driver.VmIndexMap = utils.GetVmMap()
	driver.StartDaemon()
}
