package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/mholt/archiver"
	"github.com/satori/go.uuid"
	"github.com/urfave/cli"
	"k8s.io/client-go/1.5/pkg/api"

	"github.com/fission/fission"
	"github.com/fission/fission/controller/client"
	"github.com/fission/fission/tpr"
)

func getFunctionsByPackage(client *client.Client, pkgName string) ([]tpr.Function, error) {
	fnList, err := client.FunctionList()
	if err != nil {
		return nil, err
	}
	fns := []tpr.Function{}
	for _, fn := range fnList {
		if fn.Spec.Package.PackageRef.Name == pkgName {
			fns = append(fns, fn)
		}
	}
	return fns, nil
}

func downloadArchive(client *client.Client, archive fission.Archive, fileName string) error {
	tmpDir := uuid.NewV4().String()
	tmpPath := filepath.Join(os.TempDir(), tmpDir)
	err := os.Mkdir(tmpPath, 0744)
	if err != nil {
		return err
	}

	path := filepath.Join(tmpPath, fileName+".tmp")
	if archive.Type == fission.ArchiveTypeLiteral {
		err := ioutil.WriteFile(path, archive.Literal, 0644)
		if err != nil {
			return err
		}
	} else {
		err := downloadUrl(client, archive.URL, path)
		if err != nil {
			return err
		}
	}
	newPath := filepath.Join(tmpPath, fileName)
	if archiver.Zip.Match(path) {
		newPath = filepath.Join(tmpPath, fileName+".zip")
	}
	err = os.Rename(path, newPath)
	if err != nil {
		return err
	}

	return nil
}

func downloadUrl(client *client.Client, fileUrl string, localPath string) error {
	u, err := url.ParseRequestURI(fileUrl)
	if err != nil {
		return err
	}
	// replace in-cluster storage service host with controller server url
	fileDownloadUrl := strings.TrimSuffix(client.Url, "/") + "/proxy/storage" + u.RequestURI()
	resp, err := http.Get(fileDownloadUrl)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(localPath, body, 0744)
	if err != nil {
		return err
	}

	return nil
}

func pkgCreate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	envName := c.String("env")
	if len(envName) == 0 {
		fatal("Need --env argument.")
	}

	description := c.String("desc")
	srcArchiveName := c.String("src")
	deployArchiveName := c.String("deploy")

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 {
		fatal("Need --src to specify source archive, or use --deploy to specify deployment archive.")
	}

	createPackage(client, envName, srcArchiveName, deployArchiveName, description)

	return nil
}

func pkgUpdate(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		fatal("Need --name argument.")
	}

	force := c.Bool("f")
	envName := c.String("env")
	description := c.String("desc")
	srcArchiveName := c.String("src")
	deployArchiveName := c.String("deploy")

	if len(srcArchiveName) == 0 && len(deployArchiveName) == 0 &&
		len(envName) == 0 && len(description) == 0 {
		fatal("Need --env or --desc or --src or --deploy or --desc argument.")
	}

	pkg, err := client.PackageGet(&api.ObjectMeta{
		Namespace: api.NamespaceDefault,
		Name:      pkgName,
	})
	checkErr(err, "get package")

	fnList, err := getFunctionsByPackage(client, pkgName)
	checkErr(err, "get function list")

	if !force && len(fnList) > 0 {
		fatal("Package is used by multiple functions, use -f to force update")
	}

	var srcArchiveMetadata, deployArchiveMetadata *fission.Archive
	needToBuild := false

	if len(envName) > 0 {
		pkg.Spec.Environment.Name = envName
		needToBuild = true
	}

	if len(srcArchiveName) > 0 {
		srcArchiveMetadata = createArchive(client, srcArchiveName)
		pkg.Spec.Source = *srcArchiveMetadata
		needToBuild = true
	}

	if len(deployArchiveName) > 0 {
		deployArchiveMetadata = createArchive(client, deployArchiveName)
		pkg.Spec.Deployment = *deployArchiveMetadata
	}

	if len(description) > 0 {
		pkg.Spec.Description = description
	}

	if needToBuild {
		// change into pending state to trigger package build
		pkg.Spec.Status.BuildStatus = fission.BuildStatusPending
	}

	newPkgMeta, err := client.PackageUpdate(pkg)
	checkErr(err, "update package")

	// update resource version of package reference of functions that shared the same package
	for _, fn := range fnList {
		fn.Spec.Package.PackageRef.ResourceVersion = newPkgMeta.ResourceVersion
		_, err := client.FunctionUpdate(&fn)
		checkErr(err, "update function")
	}

	return err
}

func pkgGet(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		fatal("Need name of package, use --name")
	}

	targetName := c.String("target")
	if len(targetName) == 0 {
		fmt.Println("Empty target file name, used package name instead")
	}

	pkg, err := client.PackageGet(&api.ObjectMeta{
		Namespace: api.NamespaceDefault,
		Name:      pkgName,
	})
	if err != nil {
		return err
	}

	if len(targetName) == 0 {
		targetName = pkg.Metadata.Name
	}

	if len(pkg.Spec.Source.Type) > 0 {
		err = downloadArchive(client, pkg.Spec.Source, targetName)
		if err != nil {
			fatal(fmt.Sprintf("Error downloading source archive: %v", err))
		}
	}

	if len(pkg.Spec.Deployment.Type) > 0 {
		err = downloadArchive(client, pkg.Spec.Deployment, targetName)
		if err != nil {
			fatal(fmt.Sprintf("Error downloading deployment archive: %v", err))
		}
	}

	return nil
}

func pkgInfo(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		fatal("Need name of package, use --name")
	}

	pkg, err := client.PackageGet(&api.ObjectMeta{
		Namespace: api.NamespaceDefault,
		Name:      pkgName,
	})
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\n", "Name:", pkg.Metadata.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Status:", pkg.Spec.Status.BuildStatus)
	fmt.Fprintf(w, "%v\t%v\n", "Environment:", pkg.Spec.Environment.Name)
	fmt.Fprintf(w, "%v\t%v\n", "Description:", pkg.Spec.Description)
	fmt.Fprintf(w, "%v\n%v\n", "Build Logs:\n", pkg.Spec.Status.BuildLog)
	w.Flush()

	return nil
}

func pkgList(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	pkgList, err := client.PackageList()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", "NAME", "STATUS", "ENV", "DESCRIPTION")
	for _, pkg := range pkgList {
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", pkg.Metadata.Name,
			pkg.Spec.Status.BuildStatus, pkg.Spec.Environment.Name, pkg.Spec.Description)
	}
	w.Flush()

	return nil
}

func pkgDelete(c *cli.Context) error {
	client := getClient(c.GlobalString("server"))

	pkgName := c.String("name")
	if len(pkgName) == 0 {
		fmt.Println("Need --name argument.")
		return nil
	}

	force := c.Bool("f")

	fnList, err := getFunctionsByPackage(client, pkgName)

	if !force && len(fnList) > 0 {
		fatal("Package is used by multiple functions, use -f to force delete")
	}

	err = client.PackageDelete(&api.ObjectMeta{
		Namespace: api.NamespaceDefault,
		Name:      pkgName,
	})
	if err != nil {
		return err
	}

	fmt.Printf("Package %v is deleted\n", pkgName)

	return nil
}
