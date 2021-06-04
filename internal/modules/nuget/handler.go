// SPDX-License-Identifier: Apache-2.0

package nuget

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"spdx-sbom-generator/internal/helper"
	"spdx-sbom-generator/internal/models"

	log "github.com/sirupsen/logrus"
)

type nuget struct {
	metadata   models.PluginMetadata
	rootModule *models.Module
	command    *helper.Cmd
}

var (
	packageCachePaths      []string
	dotnetCmd              = "dotnet"
	specExt                = ".nuspec"
	pkgExt                 = ".nupkg"
	sha512Ext              = ".nupkg.sha512"
	nugetBaseUrl           = "https://api.nuget.org/v3-flatcontainer/"
	manifestExtensions     = []string{".sln", ".csproj", ".vbproj"}
	directoryFilterPattern = "*.[^d-u][^c-r]proj"
	assetDirectoryJoinPath = "obj"
	assetModuleFile        = "project.assets.json"
	assetTargets           = "targets"
	assetType              = "type"
	assetPackage           = "package"
	assetDependencies      = "dependencies"
	configModuleFile       = "packages.config"
)

// New ...
func New() *nuget {
	return &nuget{
		metadata: models.PluginMetadata{
			Name:       "Nuget Package Manager",
			Slug:       "nuget",
			Manifest:   manifestExtensions,
			ModulePath: []string{},
		},
	}
}

// GetMetadata ...
func (m *nuget) GetMetadata() models.PluginMetadata {
	return m.metadata
}

// SetRootModule ...
func (m *nuget) SetRootModule(path string) error {
	module, err := m.GetRootModule(path)
	if err != nil {
		return err
	}

	m.rootModule = module
	return nil
}

// IsValid ...
func (m *nuget) IsValid(path string) bool {
	for i := range m.metadata.Manifest {
		if strings.ToLower(filepath.Ext(path)) == m.metadata.Manifest[i] {
			if helper.Exists(path) {
				return true
			}
		}
	}
	return false
}

// HasModulesInstalled ...
func (m *nuget) HasModulesInstalled(path string) error {
	// TODO: check nuGetFallBackFolderPath cache
	if err := m.buildCmd(LocalPackageCacheCmd, "."); err != nil {
		return err
	}
	globalPackageCachePath, err := m.command.Output()
	if err != nil {
		return err
	}

	if globalPackageCachePath == "" {
		return errNoDependencyCache
	}
	cachePathArray := strings.Split(globalPackageCachePath, ":")
	if len(cachePathArray) > 1 {
		packageCachePaths = append(packageCachePaths, strings.TrimSpace(cachePathArray[1]))
	}

	log.Infof("trying to restore the packages: %s", path)

	restoreCommand := command(fmt.Sprintf("%s %s", RestorePackageCmd, path))
	if err := m.buildCmd(restoreCommand, "."); err != nil {
		return err
	}

	_, err = m.command.Output()
	if err != nil {
		return err
	}

	log.Infof("looking for the project modules using location: %s", path)

	projectPaths, err := getProjectPaths(path)
	if err != nil {
		return err
	}
	// no projects found
	if len(projectPaths) == 0 {
		return errDependenciesNotFound
	}
	projectArray := []string{}
	// check asset path exists
	modulePath := filepath.Join(assetDirectoryJoinPath, assetModuleFile)
	for _, project := range projectPaths {
		projectDirectory := filepath.Dir(project)
		if helper.Exists(filepath.Join(projectDirectory, modulePath)) {
			// check asset path exists
			continue
		} else if helper.Exists(filepath.Join(projectDirectory, configModuleFile)) {
			// check config path exists
			continue
		}
		projectArray = append(projectArray, project)
	}
	if len(projectArray) == 0 {
		return nil
	}
	log.Infof("no modules found for project:%s", projectArray)
	return errDependenciesNotFound
}

// GetVersion...
func (m *nuget) GetVersion() (string, error) {
	if err := m.buildCmd(VersionCmd, "."); err != nil {
		return "", err
	}

	return m.command.Output()
}

// GetRootModule...
func (m *nuget) GetRootModule(path string) (*models.Module, error) {
	if m.rootModule == nil {
		module := models.Module{}
		for i := range m.metadata.Manifest {
			pathExtension := filepath.Ext(path)
			if strings.ToLower(pathExtension) == m.metadata.Manifest[i] {
				if helper.Exists(path) {
					// TODO: WIP for root module
					fileName := filepath.Base(path)
					rootProjectName := fileName[0 : len(fileName)-len(pathExtension)]
					module.Name = rootProjectName
					module.Root = true
				}
			}
		}
		m.rootModule = &module
	}
	return m.rootModule, nil
}

// ListModulesWithDeps ...
func (m *nuget) ListModulesWithDeps(path string) ([]models.Module, error) {
	var modules []models.Module
	projectPaths, err := getProjectPaths(path)
	if err != nil {
		return modules, err
	}
	// no projects found
	if len(projectPaths) == 0 {
		return modules, errDependenciesNotFound
	}
	modulePath := filepath.Join(assetDirectoryJoinPath, assetModuleFile)
	for _, project := range projectPaths {
		projectDirectory := filepath.Dir(project)
		if helper.Exists(filepath.Join(projectDirectory, modulePath)) {
			packages, err := parseAssetModules(filepath.Join(projectDirectory, modulePath))
			if err != nil {
				return modules, err
			}
			modules = append(modules, packages...)
			log.Infof("dependency tree completed for project(a): %s", project)
		} else if helper.Exists(filepath.Join(projectDirectory, configModuleFile)) {
			packages, err := parsePackagesConfigModules(filepath.Join(projectDirectory, configModuleFile))
			if err != nil {
				return modules, err
			}
			log.Infof("dependency tree completed for project(c): %s", project)
			modules = append(modules, packages...)
		}
	}
	if len(modules) == 0 {
		return modules, errFailedToConvertModules
	}
	return modules, nil
}

// ListUsedModules ...
func (m *nuget) ListUsedModules(path string) ([]models.Module, error) {
	return m.ListModulesWithDeps(path)
}

func (m *nuget) buildCmd(cmd command, path string) error {
	cmdArgs := cmd.Parse()
	if cmdArgs[0] != dotnetCmd {
		return errNoDotnetCommand
	}

	command := helper.NewCmd(helper.CmdOptions{
		Name:      cmdArgs[0],
		Args:      cmdArgs[1:],
		Directory: path,
	})

	m.command = command

	return command.Build()
}

// parsePackagesConfigModules parses the output -- works for the packages.config
func parsePackagesConfigModules(modulePath string) ([]models.Module, error) {
	modules := make([]models.Module, 0)
	raw, err := ioutil.ReadFile(modulePath)
	if err != nil {
		return modules, err
	}
	moduleData := PackageConfig{}
	err = xml.Unmarshal(raw, &moduleData)
	if err != nil {
		return modules, err
	}
	for _, modulePackage := range moduleData.Packages {
		module := models.Module{
			Name:    modulePackage.ID,
			Version: modulePackage.Version,
		}
		buildModule(&module)
		modules = append(modules, module)
	}
	return modules, nil
}

// parseAssetModules parses the output -- works for the project.assets.json
func parseAssetModules(modulePath string) ([]models.Module, error) {
	modules := make([]models.Module, 0)
	raw, err := ioutil.ReadFile(modulePath)
	if err != nil {
		return modules, err
	}

	moduleData := map[string]interface{}{}
	err = json.Unmarshal(raw, &moduleData)
	if err != nil {
		return modules, err
	}
	// parse targets from the asset json
	targetsData := moduleData[assetTargets].(map[string]interface{})
	if targetsData != nil {
		for _, packageData := range targetsData {
			data := packageData.(map[string]interface{})
			if data != nil {
				for name, info := range data {
					// split the package name and version
					packageArray := strings.Split(name, "/")
					packageInfo := info.(map[string]interface{})
					// consider only the package type for building the dependencies
					if packageInfo != nil &&
						packageInfo[assetType] == assetPackage && len(packageArray) == 2 {
						packageInfo := info.(map[string]interface{})
						packageName := packageArray[0]
						packageVersion := packageArray[1]
						dependencies := map[string]*models.Module{}
						// get the dependency packages
						dependencyModules := packageInfo[assetDependencies]
						if dependencyModules != nil {
							dependencyPackages := dependencyModules.(map[string]interface{})
							for dName, dInfo := range dependencyPackages {
								dVersion, ok := dInfo.(string)
								if ok {
									dependencies[dName] = &models.Module{
										Name:    dName,
										Version: dVersion,
									}
								}
							}
						}
						module := models.Module{
							Name:    packageName,
							Version: packageVersion,
							Modules: dependencies,
						}
						buildModule(&module)
						modules = append(modules, module)
					}
				}
			}
		}
	}
	return modules, nil
}

// getProjectPaths
func getProjectPaths(path string) ([]string, error) {
	var projectPath []string
	directoryPath := filepath.Dir(path)
	err := filepath.Walk(directoryPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if matched, err := filepath.Match(directoryFilterPattern, filepath.Base(path)); err != nil {
			return err
		} else if matched {
			projectPath = append(projectPath, path)
		}
		return nil
	})
	if err != nil {
		return projectPath, err
	}
	return projectPath, nil
}

// buildModule .. set the properties
func buildModule(module *models.Module) error {
	// get nuget spec file details
	nuSpecFile, err := getNugetSpec(module.Name, module.Version)
	if err != nil {
		return err
	}
	// get the hash checksum
	checkSum, err := getHashCheckSum(module.Name, module.Version)
	if err != nil {
		return err
	}
	if checkSum != nil {
		module.CheckSum = checkSum
	}
	if nuSpecFile != nil {
		module.PackageHomePage = nuSpecFile.Meta.ProjectURL
		module.LicenseDeclared = nuSpecFile.Meta.LicenseURL
		module.Copyright = nuSpecFile.Meta.Copyright
		module.CommentsLicense = nuSpecFile.Meta.License.Text
		module.Supplier.Name = nuSpecFile.Meta.Owners
		// TODO -- identify other properties
	}
	return nil
}

// getCachedSpecFilename
func getCachedSpecFilename(name string, version string) string {
	var specFilename string
	if name == "" || version == "" {
		return specFilename
	}
	name = strings.ToLower(name)
	for _, path := range packageCachePaths {
		var directory = filepath.Join(path, name, version)
		var fileName = filepath.Join(directory, fmt.Sprintf("%s%s", name, specExt))
		if helper.Exists(fileName) {
			specFilename = fileName
			break
		}
	}
	return specFilename
}

// getNugetSpec ...
func getNugetSpec(name string, version string) (*NugetSpec, error) {
	nuSpecFile := NugetSpec{}
	specFileName := getCachedSpecFilename(name, version)
	if specFileName != "" {
		raw, err := ioutil.ReadFile(specFileName)
		if err != nil {
			return nil, err
		}
		specFile, err := ConvertFromBytes(raw)
		if err != nil {
			return nil, err
		}
		return specFile, nil
	}
	nugetUrlPrefix := fmt.Sprintf("%s%s/%s/%s", nugetBaseUrl, name, version, name)
	nuspecUrl := fmt.Sprintf("%s%s", nugetUrlPrefix, specExt)
	resp, err := getHttpResponseWithHeaders(nuspecUrl, map[string]string{"content-type": "application/xml"})
	if err != nil {
		return nil, err
	}

	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			log.Error(fmt.Sprintf("%#v", err))
		}
	}()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	err = xml.Unmarshal(body, &nuSpecFile)
	if err != nil {
		return nil, err
	}
	return &nuSpecFile, nil
}

// getHashCheckSum ...
func getHashCheckSum(name string, version string) (*models.CheckSum, error) {
	var fileData []byte
	specFileName := getCachedSpecFilename(name, version)
	if specFileName != "" {
		extension := filepath.Ext(specFileName)
		// extract the file name
		fileName := specFileName[0 : len(specFileName)-len(extension)]
		// change the extension - sha512Ext
		shaName := fmt.Sprintf("%s.%s%s", fileName, version, sha512Ext)
		// change the extension - pkgExt
		pkgName := fileName + fmt.Sprintf("%s.%s%s", fileName, version, pkgExt)
		if helper.Exists(shaName) {
			shaFileData, err := ioutil.ReadFile(shaName)
			if err != nil {
				return nil, err
			}
			fileData = shaFileData
		} else if helper.Exists(pkgName) {
			shaFileData, err := ioutil.ReadFile(pkgName)
			if err != nil {
				return nil, err
			}
			fileData = shaFileData
		}
	}
	if fileData != nil {
		return &models.CheckSum{
			Algorithm: models.HashAlgoSHA1,
			Value:     readCheckSum(fileData),
		}, nil
	}
	nugetUrlPrefix := fmt.Sprintf("%s%s/%s/%s", nugetBaseUrl, name, version, name)
	nuPkgUrl := fmt.Sprintf("%s.%s%s", nugetUrlPrefix, version, pkgExt)
	resp, err := getHttpResponseWithHeaders(nuPkgUrl, map[string]string{"content-type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			log.Error(fmt.Sprintf("%#v", err))
		}
	}()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		return &models.CheckSum{
			Algorithm: models.HashAlgoSHA1,
			Value:     readCheckSum(body),
		}, nil
	}
	return nil, nil
}
