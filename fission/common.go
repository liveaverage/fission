/*
Copyright 2016 The Fission Authors.

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

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"strings"

	uuid "github.com/satori/go.uuid"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	storageSvcClient "github.com/fission/fission/storagesvc/client"
	"github.com/fission/fission/tpr"
)

func fatal(msg string) {
	os.Stderr.WriteString(msg + "\n")
	os.Exit(1)
}

func getClient(serverUrl string) *client.Client {

	if len(serverUrl) == 0 {
		fatal("Need --server or FISSION_URL set to your fission server.")
	}

	isHTTPS := strings.Index(serverUrl, "https://") == 0
	isHTTP := strings.Index(serverUrl, "http://") == 0

	if !(isHTTP || isHTTPS) {
		serverUrl = "http://" + serverUrl
	}

	return client.MakeClient(serverUrl)
}

func checkErr(err error, msg string) {
	if err != nil {
		fatal(fmt.Sprintf("Failed to %v: %v", msg, err))
	}
}

func fileSize(filePath string) int64 {
	info, err := os.Stat(filePath)
	checkErr(err, fmt.Sprintf("stat %v", filePath))
	return info.Size()
}

// upload a file and return a fission.Archive
func createArchive(client *client.Client, fileName string) *fission.Archive {
	var archive fission.Archive
	if fileSize(fileName) < fission.ArchiveLiteralSizeLimit {
		contents := getContents(fileName)
		archive.Type = fission.ArchiveTypeLiteral
		archive.Literal = contents
	} else {
		u := strings.TrimSuffix(client.Url, "/") + "/proxy/storage"
		ssClient := storageSvcClient.MakeClient(u)

		// TODO add a progress bar
		id, err := ssClient.Upload(fileName, nil)
		checkErr(err, fmt.Sprintf("upload file %v", fileName))

		archiveUrl := ssClient.GetUrl(id)

		archive.Type = fission.ArchiveTypeUrl
		archive.URL = archiveUrl
	}
	return &archive
}

func createPackage(client *client.Client, envName, srcPkgName, deployPkgName, description string) *api.ObjectMeta {
	pkgSpec := fission.PackageSpec{
		Environment: fission.EnvironmentReference{
			Namespace: api.NamespaceDefault,
			Name:      envName,
		},
		Description: description,
	}
	var pkgStatus fission.BuildStatus = fission.BuildStatusSucceeded

	if len(deployPkgName) > 0 {
		pkgSpec.Deployment = *createArchive(client, deployPkgName)
		if len(srcPkgName) > 0 {
			fmt.Println("Deployment may be overwritten by builder manager after source package compilation")
		}
	}
	if len(srcPkgName) > 0 {
		pkgSpec.Source = *createArchive(client, srcPkgName)
		// set pending status to package
		pkgStatus = fission.BuildStatusPending
	}

	pkgSpec.Status = fission.PackageStatus{
		BuildStatus: pkgStatus,
	}

	pkgName := strings.ToLower(uuid.NewV4().String())
	pkg := &tpr.Package{
		Metadata: api.ObjectMeta{
			Name:      pkgName,
			Namespace: api.NamespaceDefault,
		},
		Spec: pkgSpec,
	}
	pkgMetadata, err := client.PackageCreate(pkg)
	checkErr(err, "create package")

	fmt.Printf("package '%v' created\n", pkgMetadata.Name)

	return pkgMetadata
}

func getContents(filePath string) []byte {
	var code []byte
	var err error

	code, err = ioutil.ReadFile(filePath)
	checkErr(err, fmt.Sprintf("read %v", filePath))
	return code
}
