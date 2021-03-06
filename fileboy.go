package main

import (
	"fmt"
	"gopkg.in/fsnotify/fsnotify.v1"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	Version = 1

	PreError = "ERROR:"
	PreWarn  = "Warn:"
)

var (
	projectFolder = "."

	filegirlYamlName = "filegirl.yaml"

	cfg *FileGirl

	watcher *fsnotify.Watcher

	taskMan *TaskMan

	ioeventMapStr = map[fsnotify.Op]string{
		fsnotify.Write:  "write",
		fsnotify.Rename: "rename",
		fsnotify.Remove: "remove",
		fsnotify.Create: "create",
		fsnotify.Chmod:  "chmod",
	}
)

type changedFile struct {
	Name    string
	Changed int64
	Ext     string
	Event   string
}

func parseConfig() {
	cfg = new(FileGirl)
	fc, err := ioutil.ReadFile(getFileGirlPath())
	if err != nil {
		log.Println(PreError, "The filegirl.yaml file in", projectFolder, "is not exist! ", err)
		fmt.Print(firstRunHelp)
		logAndExit("Fileboy unable to run.")
	}
	err = yaml.Unmarshal(fc, cfg)
	if err != nil {
		logAndExit(PreError, "Parsed filegirl.yaml failed: ", err)
	}
	if cfg.Core.Version > Version {
		logAndExit(PreError, "Current fileboy support max version : ", Version)
	}
	// init map
	cfg.Monitor.TypesMap = map[string]bool{}
	cfg.Monitor.IncludeDirsMap = map[string]bool{}
	cfg.Monitor.ExceptDirsMap = map[string]bool{}
	cfg.Monitor.IncludeDirsRec = map[string]bool{}
	// convert to map
	for _, v := range cfg.Monitor.Types {
		cfg.Monitor.TypesMap[v] = true
	}
	log.Println(cfg)
}

func eventDispatcher(event fsnotify.Event) {
	ext := path.Ext(event.Name)
	if len(cfg.Monitor.Types) > 0 &&
		!keyInMonitorTypesMap(".*", cfg) &&
		!keyInMonitorTypesMap(ext, cfg) {
		return
	}

	op := ioeventMapStr[event.Op]
	if len(cfg.Monitor.Events) != 0 && !inStrArray(op, cfg.Monitor.Events) {
		return
	}
	log.Println("EVENT", event.Op.String(), ":", event.Name)
	taskMan.Put(&changedFile{
		Name:    relativePath(projectFolder, event.Name),
		Changed: time.Now().UnixNano(),
		Ext:     ext,
		Event:   op,
	})
}

func addWatcher() {
	log.Println("collecting directory information...")
	dirsMap := map[string]bool{}
	for _, dir := range cfg.Monitor.IncludeDirs {
		darr := dirParse2Array(dir)
		if len(darr) < 1 || len(darr) > 2 {
			logAndExit(PreError, "filegirl section monitor dirs is error. ", dir)
		}
		if strings.HasPrefix(darr[0], "/") {
			logAndExit(PreError, "dirs must be relative paths ! err path:", dir)
		}
		if darr[0] == "." {
			if len(darr) == 2 && darr[1] == "*" {
				// The highest priority
				dirsMap = map[string]bool{
					projectFolder: true,
				}
				listFile(projectFolder, func(d string) {
					dirsMap[d] = true
				})
				cfg.Monitor.IncludeDirsRec[projectFolder] = true
				break
			} else {
				dirsMap[projectFolder] = true
			}
		} else {
			md := projectFolder + "/" + darr[0]
			dirsMap[md] = true
			if len(darr) == 2 && darr[1] == "*" {
				listFile(md, func(d string) {
					dirsMap[d] = true
				})
				cfg.Monitor.IncludeDirsRec[md] = true
			}
		}

	}
	for _, dir := range cfg.Monitor.ExceptDirs {
		if dir == "." {
			logAndExit(PreError, "exceptDirs must is not project root path ! err path:", dir)
		}
		p := projectFolder + "/" + dir
		delete(dirsMap, p)
		listFile(p, func(d string) {
			delete(dirsMap, d)
		})
	}
	for dir := range dirsMap {
		log.Println("watcher add -> ", dir)
		err := watcher.Add(dir)
		if err != nil {
			logAndExit(PreError, err)
		}
	}
	log.Println("total monitored dirs: " + strconv.Itoa(len(dirsMap)))
	log.Println("fileboy is ready.")
	cfg.Monitor.DirsMap = dirsMap
}

func initWatcher() {
	var err error
	if watcher != nil {
		_ = watcher.Close()
	}
	watcher, err = fsnotify.NewWatcher()
	if err != nil {
		logAndExit(err)
	}
	taskMan = newTaskMan(cfg.Command.DelayMillSecond, cfg.Notifier.CallUrl)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// directory structure changes, dynamically add, delete and monitor according to rules
				// TODO // this method cannot be triggered when the parent folder of the change folder is not monitored
				go watchChangeHandler(event)
				eventDispatcher(event)
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println(PreError, err)
			}
		}
	}()
	addWatcher()
}

func watchChangeHandler(event fsnotify.Event) {
	if event.Op != fsnotify.Create && event.Op != fsnotify.Rename {
		return
	}
	_, err := ioutil.ReadDir(event.Name)
	if err != nil {
		return
	}
	do := false
	for rec := range cfg.Monitor.IncludeDirsRec {
		if !strings.HasPrefix(event.Name, rec) {
			continue
		}
		// check exceptDirs
		has := false
		for _, v := range cfg.Monitor.ExceptDirs {
			if strings.HasPrefix(event.Name, projectFolder+"/"+v) {
				has = true
			}
		}
		if has {
			continue
		}

		_ = watcher.Remove(event.Name)
		err := watcher.Add(event.Name)
		if err == nil {
			do = true
			log.Println("watcher add -> ", event.Name)
		} else {
			log.Println(PreWarn, "watcher add faild:", event.Name, err)
		}
	}

	if do {
		return
	}

	// check map
	if _, ok := cfg.Monitor.DirsMap[event.Name]; ok {
		_ = watcher.Remove(event.Name)
		err := watcher.Add(event.Name)
		if err == nil {
			log.Println("watcher add -> ", event.Name)
		} else {
			log.Println(PreWarn, "watcher add faild:", event.Name, err)
		}
	}
}

func parseArgs() {
	switch len(os.Args) {
	case 1:
		parseConfig()
		done := make(chan bool)
		initWatcher()
		defer watcher.Close()
		<-done
		return
	case 2:
		c := os.Args[1]
		switch c {
		case "init":
			_, err := ioutil.ReadFile(getFileGirlPath())
			if err == nil {
				log.Println(PreError, "Profile filegirl.yaml already exists.")
				logAndExit("If you want to regenerate filegirl.yaml, delete it first")
			}
			err = ioutil.WriteFile(getFileGirlPath(), []byte(exampleFileGirl), 0644)
			if err != nil {
				log.Println(PreError, "Profile filegirl.yaml create failed! ", err)
				return
			}
			log.Println("Profile filegirl.yaml created ok")
			return
		case "exec":
			parseConfig()
			newTaskMan(0, cfg.Notifier.CallUrl).run(new(changedFile))
			return
		case "version", "v", "-v", "--version":
			fmt.Println(versionDesc)
		default:
			fmt.Print(helpStr)
		}
		return
	default:
		logAndExit("Unknown parameters, use `fileboy help` show help info.")
	}
}

func getFileGirlPath() string {
	return projectFolder + "/" + filegirlYamlName
}

func show() {
	fmt.Print(logo)
	rand.Seed(time.Now().UnixNano())
	fmt.Println(englishSay[rand.Intn(len(englishSay))])
	fmt.Println("")
	fmt.Println(statement)
}

func main() {
	log.SetPrefix("[FileBoy]: ")
	log.SetFlags(2)
	log.SetOutput(os.Stdout)
	show()
	var err error
	projectFolder, err = os.Getwd()
	if err != nil {
		logAndExit(err)
	}
	parseArgs()
}
