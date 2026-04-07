//go:build cgo
// +build cgo

package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// FAT16 константы
const (
	FAT_FREE      uint16 = 0x0000
	FAT_EOF       uint16 = 0xFFFF
	FAT_BAD       uint16 = 0xFFF7
	FAT_EOF_LOWER uint16 = 0xFFF8
	FAT_EOF_UPPER uint16 = 0xFFFF

	CLUSTER_DATA_START = 2

	BOOT_SECTOR_SIZE           = 512
	BYTES_PER_SECTOR_OFFSET    = 11
	SECTORS_PER_CLUSTER_OFFSET = 13
	RESERVED_SECTORS_OFFSET    = 14
	NUMBER_OF_FATS_OFFSET      = 16
	ROOT_ENTRIES_OFFSET        = 17
	SECTORS_PER_FAT_OFFSET     = 22
	SECTORS_PER_FAT_EXT_OFFSET = 36

	DIR_ENTRY_SIZE           = 32
	DIR_ENTRY_EMPTY          = 0x00
	DIR_ENTRY_DELETED        = 0xE5
	DIR_ATTR_DIRECTORY       = 0x10
	DIR_ATTR_VOLUME_LABEL    = 0x08
	DIR_ATTR_LONG_NAME       = 0x0F
	DIR_NAME_OFFSET          = 0
	DIR_NAME_LENGTH          = 8
	DIR_EXT_OFFSET           = 8
	DIR_EXT_LENGTH           = 3
	DIR_ATTR_OFFSET          = 11
	DIR_START_CLUSTER_OFFSET = 26
	DIR_FILE_SIZE_OFFSET     = 28

	BYTES_PER_CLUSTER_ENTRY = 2
)

// FileInfo информация о файле
type FileInfo struct {
	Name         string `json:"name"`
	StartCluster int    `json:"start_cluster"`
	Size         int    `json:"size"`
	IsDirectory  bool   `json:"is_directory"`
}

// Damage информация о повреждении
type Damage struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Cluster     int    `json:"cluster"`
	OldValue    uint16 `json:"old_value"`
	NewValue    uint16 `json:"new_value"`
}

// Intersection пересечение кластеров
type Intersection struct {
	Cluster int      `json:"cluster"`
	Files   []string `json:"files"`
}

// Loop зацикленность
type Loop struct {
	StartCluster int    `json:"start_cluster"`
	FileName     string `json:"file_name"`
}

// FAT16Instance экземпляр FAT16
type FAT16Instance struct {
	mu                sync.RWMutex
	file              *os.File
	filename          string
	fatTable          []uint16
	files             []FileInfo
	bytesPerSector    uint16
	reservedSectors   uint16
	sectorsPerFAT     uint16
	sectorsPerCluster uint8
	numberOfFATs      uint8
}

var (
	instances = make(map[int]*FAT16Instance)
	nextID    = 1
	instMu    sync.Mutex
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// loadFAT16 загружает FAT16 образ
func loadFAT16(filename string) (*FAT16Instance, error) {
	file, err := os.OpenFile(filename, os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия: %v", err)
	}

	inst := &FAT16Instance{
		file:     file,
		filename: filename,
	}

	// Читаем boot sector
	bootSector := make([]byte, BOOT_SECTOR_SIZE)
	if _, err := file.Read(bootSector); err != nil {
		file.Close()
		return nil, err
	}

	inst.bytesPerSector = binary.LittleEndian.Uint16(bootSector[BYTES_PER_SECTOR_OFFSET:][:2])
	inst.sectorsPerCluster = bootSector[SECTORS_PER_CLUSTER_OFFSET]
	inst.reservedSectors = binary.LittleEndian.Uint16(bootSector[RESERVED_SECTORS_OFFSET:][:2])
	inst.numberOfFATs = bootSector[NUMBER_OF_FATS_OFFSET]
	rootEntries := binary.LittleEndian.Uint16(bootSector[ROOT_ENTRIES_OFFSET:][:2])
	inst.sectorsPerFAT = binary.LittleEndian.Uint16(bootSector[SECTORS_PER_FAT_OFFSET:][:2])

	if inst.sectorsPerFAT == 0 {
		inst.sectorsPerFAT = binary.LittleEndian.Uint16(bootSector[SECTORS_PER_FAT_EXT_OFFSET:][:2])
	}

	// Загружаем FAT таблицу
	if err := inst.loadFATTable(); err != nil {
		file.Close()
		return nil, err
	}

	// Читаем корневую директорию
	inst.files = inst.readRootDirectory(rootEntries)

	return inst, nil
}

// loadFATTable загружает FAT таблицу
func (f *FAT16Instance) loadFATTable() error {
	fatOffset := int64(f.reservedSectors) * int64(f.bytesPerSector)
	if _, err := f.file.Seek(fatOffset, 0); err != nil {
		return err
	}

	fatSize := int64(f.sectorsPerFAT) * int64(f.bytesPerSector) / BYTES_PER_CLUSTER_ENTRY
	f.fatTable = make([]uint16, fatSize)

	for i := int64(0); i < fatSize; i++ {
		if err := binary.Read(f.file, binary.LittleEndian, &f.fatTable[i]); err != nil {
			f.fatTable[i] = FAT_FREE
		}
	}
	return nil
}

// readRootDirectory читает корневую директорию
func (f *FAT16Instance) readRootDirectory(rootEntries uint16) []FileInfo {
	var files []FileInfo

	fatSize := int64(f.sectorsPerFAT) * int64(f.bytesPerSector)
	rootOffset := int64(f.reservedSectors)*int64(f.bytesPerSector) + int64(f.numberOfFATs)*fatSize
	f.file.Seek(rootOffset, 0)

	for i := 0; i < int(rootEntries); i++ {
		var entry [DIR_ENTRY_SIZE]byte
		if _, err := f.file.Read(entry[:]); err != nil {
			break
		}

		if entry[0] == DIR_ENTRY_EMPTY || entry[0] == DIR_ENTRY_DELETED {
			continue
		}

		attr := entry[DIR_ATTR_OFFSET]
		if attr&DIR_ATTR_VOLUME_LABEL != 0 || attr&DIR_ATTR_LONG_NAME == DIR_ATTR_LONG_NAME {
			continue
		}

		name := strings.TrimRight(string(entry[DIR_NAME_OFFSET:DIR_NAME_OFFSET+DIR_NAME_LENGTH]), " \x00")
		ext := strings.TrimRight(string(entry[DIR_EXT_OFFSET:DIR_EXT_OFFSET+DIR_EXT_LENGTH]), " \x00")

		fullName := name
		if ext != "" && ext != "   " {
			fullName = name + "." + ext
		}

		startCluster := int(binary.LittleEndian.Uint16(entry[DIR_START_CLUSTER_OFFSET:][:2]))
		fileSize := binary.LittleEndian.Uint32(entry[DIR_FILE_SIZE_OFFSET:][:4])

		files = append(files, FileInfo{
			Name:         fullName,
			StartCluster: startCluster,
			Size:         int(fileSize),
			IsDirectory:  (attr & DIR_ATTR_DIRECTORY) != 0,
		})
	}
	return files
}

// saveFATTable сохраняет FAT таблицу
func (f *FAT16Instance) saveFATTable() error {
	fatOffset := int64(f.reservedSectors) * int64(f.bytesPerSector)
	if _, err := f.file.Seek(fatOffset, 0); err != nil {
		return err
	}

	for i := 0; i < len(f.fatTable); i++ {
		if err := binary.Write(f.file, binary.LittleEndian, f.fatTable[i]); err != nil {
			return err
		}
	}

	// Копируем во вторую FAT таблицу
	secondFATOffset := fatOffset + int64(f.sectorsPerFAT)*int64(f.bytesPerSector)
	if _, err := f.file.Seek(secondFATOffset, 0); err != nil {
		return err
	}

	for i := 0; i < len(f.fatTable); i++ {
		if err := binary.Write(f.file, binary.LittleEndian, f.fatTable[i]); err != nil {
			return err
		}
	}

	return nil
}

// getChain получает цепочку кластеров
func (f *FAT16Instance) getChain(start int) []int {
	var chain []int
	visited := make(map[int]bool)
	current := start

	for current >= CLUSTER_DATA_START && current < len(f.fatTable) && !visited[current] {
		visited[current] = true
		chain = append(chain, current)

		val := f.fatTable[current]
		if val == FAT_EOF || (val >= FAT_EOF_LOWER && val <= FAT_EOF_UPPER) {
			break
		}
		if val == FAT_FREE || int(val) < CLUSTER_DATA_START || int(val) >= len(f.fatTable) {
			break
		}
		current = int(val)
	}
	return chain
}

// findFreeClusters находит свободные кластеры
func (f *FAT16Instance) findFreeClusters(count int) []int {
	var free []int
	for i := CLUSTER_DATA_START; i < len(f.fatTable) && len(free) < count; i++ {
		if f.fatTable[i] == FAT_FREE {
			free = append(free, i)
		}
	}
	return free
}

// copyClusterData копирует данные между кластерами
func (f *FAT16Instance) copyClusterData(oldClusters, newClusters []int) error {
	if len(oldClusters) != len(newClusters) {
		return fmt.Errorf("разное количество кластеров")
	}

	clusterSize := int64(f.bytesPerSector) * int64(f.sectorsPerCluster)
	fatSize := int64(f.sectorsPerFAT) * int64(f.bytesPerSector)
	dataStart := int64(f.reservedSectors)*int64(f.bytesPerSector) + int64(f.numberOfFATs)*fatSize

	buf := make([]byte, clusterSize)

	for i := 0; i < len(oldClusters); i++ {
		oldOff := dataStart + int64(oldClusters[i]-CLUSTER_DATA_START)*clusterSize
		if _, err := f.file.Seek(oldOff, 0); err != nil {
			return err
		}
		if _, err := f.file.Read(buf); err != nil {
			return err
		}

		newOff := dataStart + int64(newClusters[i]-CLUSTER_DATA_START)*clusterSize
		if _, err := f.file.Seek(newOff, 0); err != nil {
			return err
		}
		if _, err := f.file.Write(buf); err != nil {
			return err
		}
	}
	return nil
}

// CreateMissingEOF создает повреждение "отсутствие EOF"
func (f *FAT16Instance) CreateMissingEOF() (*Damage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Находим файлы с правильным EOF
	var candidates []struct {
		file        FileInfo
		lastCluster int
	}

	for _, file := range f.files {
		if file.StartCluster < CLUSTER_DATA_START || file.StartCluster >= len(f.fatTable) {
			continue
		}
		chain := f.getChain(file.StartCluster)
		if len(chain) == 0 {
			continue
		}
		last := chain[len(chain)-1]
		val := f.fatTable[last]
		if val == FAT_EOF || (val >= FAT_EOF_LOWER && val <= FAT_EOF_UPPER) {
			candidates = append(candidates, struct {
				file        FileInfo
				lastCluster int
			}{file, last})
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("нет файлов с EOF")
	}

	selectedIdx := rand.Intn(len(candidates))
	selected := candidates[selectedIdx]

	oldValue := f.fatTable[selected.lastCluster]

	// Ищем следующий кластер для "разрыва"
	free := f.findFreeClusters(1)
	if len(free) > 0 {
		f.fatTable[selected.lastCluster] = uint16(free[0])
		f.saveFATTable()
		return &Damage{
			Type:        "missing_eof",
			Description: fmt.Sprintf("Удален EOF: файл %s, кластер %d теперь указывает на %d", selected.file.Name, selected.lastCluster, free[0]),
			Cluster:     selected.lastCluster,
			OldValue:    oldValue,
			NewValue:    uint16(free[0]),
		}, nil
	}

	return nil, fmt.Errorf("нет свободных кластеров")
}

// CreateIntersection создает пересечение кластеров (случайное)
func (f *FAT16Instance) CreateIntersection() (*Damage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Находим файлы с цепочками (минимум 3 кластера для интересных пересечений)
	var filesWithChains []struct {
		file  FileInfo
		chain []int
	}

	for _, file := range f.files {
		if file.StartCluster >= CLUSTER_DATA_START && file.StartCluster < len(f.fatTable) && !file.IsDirectory {
			chain := f.getChain(file.StartCluster)
			// Требуем минимум 3 кластера для более интересных пересечений
			if len(chain) >= 3 {
				filesWithChains = append(filesWithChains, struct {
					file  FileInfo
					chain []int
				}{file, chain})
			}
		}
	}

	if len(filesWithChains) < 2 {
		return nil, fmt.Errorf("нужно минимум 2 файла с цепочкой из 3+ кластеров")
	}

	// Выбираем два разных файла случайно
	idx1 := rand.Intn(len(filesWithChains))
	idx2 := rand.Intn(len(filesWithChains))
	for idx2 == idx1 {
		idx2 = rand.Intn(len(filesWithChains))
	}

	f1 := filesWithChains[idx1]
	f2 := filesWithChains[idx2]

	// Выбираем случайный кластер из первого файла (цель)
	targetIdx := rand.Intn(len(f1.chain))
	target := f1.chain[targetIdx]

	// Выбираем случайный кластер из второго файла (источник)
	// Не берем последний, чтобы не нарушать EOF
	sourceIdx := rand.Intn(len(f2.chain) - 1)
	source := f2.chain[sourceIdx]

	// Если случайно выбрали один и тот же кластер, пробуем другой
	if source == target && len(f2.chain) > 1 {
		sourceIdx = (sourceIdx + 1) % (len(f2.chain) - 1)
		source = f2.chain[sourceIdx]
	}

	oldValue := f.fatTable[source]

	// Создаем пересечение: кластер из второго файла указывает на кластер из первого
	f.fatTable[source] = uint16(target)
	f.saveFATTable()

	// Формируем описание повреждения
	description := fmt.Sprintf("Пересечение: файл '%s' (кластер %d) теперь указывает на кластер %d из файла '%s'",
		f2.file.Name, source, target, f1.file.Name)

	// Добавляем информацию о цепочках
	if len(f2.chain) > 1 {
		description += fmt.Sprintf("\n  Цепочка %s была: %v → теперь: ", f2.file.Name, f2.chain)
		newChain := f.getChain(f2.file.StartCluster)
		description += fmt.Sprintf("%v", newChain)
	}

	return &Damage{
		Type:        "intersection",
		Description: description,
		Cluster:     source,
		OldValue:    oldValue,
		NewValue:    uint16(target),
	}, nil
}

// CreateLoop создает зацикленность (случайный файл)
func (f *FAT16Instance) CreateLoop() (*Damage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Находим файлы с длинными цепочками (минимум 4 кластера для создания цикла)
	var filesWithChains []struct {
		file  FileInfo
		chain []int
	}

	for _, file := range f.files {
		if file.StartCluster >= CLUSTER_DATA_START && file.StartCluster < len(f.fatTable) && !file.IsDirectory {
			chain := f.getChain(file.StartCluster)
			// Требуем минимум 4 кластера для создания интересного цикла
			if len(chain) >= 4 {
				filesWithChains = append(filesWithChains, struct {
					file  FileInfo
					chain []int
				}{file, chain})
			}
		}
	}

	if len(filesWithChains) == 0 {
		return nil, fmt.Errorf("нет файлов с цепочкой из 4+ кластеров")
	}

	// Выбираем случайный файл
	selectedIdx := rand.Intn(len(filesWithChains))
	selected := filesWithChains[selectedIdx]
	chain := selected.chain

	createType := rand.Intn(2)

	var toChange int // кластер, который будем перенаправлять
	var target int   // кластер, на который будем указывать
	var description string

	if createType == 0 && len(chain) >= 3 {
		lastIdx := len(chain) - 1
		firstIdx := 0

		toChange = chain[lastIdx]
		target = chain[firstIdx]
		description = fmt.Sprintf("Зацикленность: файл %s, последний кластер %d указывает на первый кластер %d",
			selected.file.Name, toChange, target)
	} else {
		if len(chain) >= 4 {
			// Выбираем случайный кластер для перенаправления (не первый и не последний)
			sourceIdx := rand.Intn(len(chain)-2) + 1 // от 1 до len-2
			// Выбираем целевой кластер, который находится раньше sourceIdx (но не предыдущий)
			targetIdx := rand.Intn(sourceIdx) // от 0 до sourceIdx-1

			// Убеждаемся, что targetIdx не равен sourceIdx-1 (чтобы не создать просто обрыв)
			if targetIdx == sourceIdx-1 && sourceIdx > 1 {
				targetIdx = rand.Intn(sourceIdx - 1)
			}

			toChange = chain[sourceIdx]
			target = chain[targetIdx]
			description = fmt.Sprintf("Зацикленность: файл %s, кластер %d указывает на более ранний кластер %d",
				selected.file.Name, toChange, target)
		} else {
			// Если цепочка короткая, используем первый способ
			lastIdx := len(chain) - 1
			firstIdx := 0
			toChange = chain[lastIdx]
			target = chain[firstIdx]
			description = fmt.Sprintf("Зацикленность: файл %s, последний кластер %d указывает на первый кластер %d",
				selected.file.Name, toChange, target)
		}
	}

	// Проверяем, что toChange и target разные
	if toChange == target {
		// Если случайно выбрали одинаковые, пробуем другой вариант
		lastIdx := len(chain) - 1
		firstIdx := 0
		toChange = chain[lastIdx]
		target = chain[firstIdx]
		description = fmt.Sprintf("Зацикленность: файл %s, последний кластер %d указывает на первый кластер %d",
			selected.file.Name, toChange, target)
	}

	oldValue := f.fatTable[toChange]
	f.fatTable[toChange] = uint16(target)
	f.saveFATTable()

	return &Damage{
		Type:        "loop",
		Description: description,
		Cluster:     toChange,
		OldValue:    oldValue,
		NewValue:    uint16(target),
	}, nil
}

// CheckAndFix проверяет и исправляет файловую систему
// Возвращает: список кластеров без EOF, список пересечений, список циклов, список исправлений, ошибку
func (f *FAT16Instance) CheckAndFix() ([]int, []Intersection, []Loop, []Damage, error) {
	// Блокируем мьютекс для потокобезопасности
	f.mu.Lock()
	defer f.mu.Unlock()

	// Слайсы для сбора результатов
	var missingEOF []int             // Кластеры, у которых отсутствует маркер конца файла
	var intersections []Intersection // Найденные пересечения кластеров
	var loops []Loop                 // Найденные циклы в цепочках
	var fixes []Damage               // Выполненные исправления

	// ===================================
	// PART 1: ИСПРАВЛЕНИЕ ОТСУТСТВИЯ EOF
	// ===================================
	// Проходим по всем файлам и проверяем, есть ли у последнего кластера маркер EOF
	for _, file := range f.files {
		// Пропускаем файлы с некорректным стартовым кластером
		if file.StartCluster < CLUSTER_DATA_START || file.StartCluster >= len(f.fatTable) {
			continue
		}

		// Получаем цепочку кластеров файла
		chain := f.getChain(file.StartCluster)
		if len(chain) == 0 {
			continue
		}

		// Берем последний кластер в цепочке
		last := chain[len(chain)-1]
		val := f.fatTable[last]

		// Если последний кластер не имеет маркера EOF (0xFFF8-0xFFFF) и не поврежден
		if val != FAT_EOF && (val < FAT_EOF_LOWER || val > FAT_EOF_UPPER) && val != FAT_BAD {
			missingEOF = append(missingEOF, last)
			oldValue := f.fatTable[last]
			// Устанавливаем маркер EOF
			f.fatTable[last] = FAT_EOF
			// Записываем исправление в лог
			fixes = append(fixes, Damage{
				Type:        "fix_missing_eof",
				Description: fmt.Sprintf("Исправлен EOF: файл %s, кластер %d", file.Name, last),
				Cluster:     last,
				OldValue:    oldValue,
				NewValue:    FAT_EOF,
			})
		}
	}

	// =============================================
	// PART 2: НАХОЖДЕНИЕ И ИСПРАВЛЕНИЕ ПЕРЕСЕЧЕНИЙ
	// =============================================

	// Шаг 2.1: Построение карты использования кластеров
	// --------------------------------------------------
	// clusterToFiles: для каждого кластера список файлов, которые его используют
	// fileChains: для каждого файла его цепочка кластеров
	// fileInfo: информация о файлах для быстрого доступа
	clusterToFiles := make(map[int][]string)
	fileChains := make(map[string][]int)
	fileInfo := make(map[string]FileInfo)

	for _, file := range f.files {
		// Пропускаем файлы с некорректным стартовым кластером
		if file.StartCluster < CLUSTER_DATA_START || file.StartCluster >= len(f.fatTable) {
			continue
		}
		// Получаем цепочку кластеров файла
		chain := f.getChain(file.StartCluster)
		fileChains[file.Name] = chain
		fileInfo[file.Name] = file

		// Удаляем дубликаты кластеров в цепочке (каждый кластер учитываем один раз)
		unique := make(map[int]bool)
		for _, c := range chain {
			unique[c] = true
		}
		// Заполняем карту: кластер -> список файлов, использующих его
		for c := range unique {
			clusterToFiles[c] = append(clusterToFiles[c], file.Name)
		}
	}

	// Шаг 2.2: Запись всех найденных пересечений в результат
	// -------------------------------------------------------
	// Пересечение = кластер, который используется более чем одним файлом
	for cluster, files := range clusterToFiles {
		if len(files) > 1 {
			intersections = append(intersections, Intersection{
				Cluster: cluster,
				Files:   files,
			})
		}
	}

	// Шаг 2.3: Исправление пересечений
	// ---------------------------------
	// fixedFiles: файлы, которые уже были исправлены (чтобы не обрабатывать повторно)
	fixedFiles := make(map[string]bool)
	needRescan := true // Флаг необходимости повторного сканирования
	maxIterations := 1 // Защита от бесконечного цикла
	iteration := 0

	// Цикл пересканирования - продолжаем исправлять, пока есть пересечения
	// Важно, т. к. исправление одного пересечения может создать новые
	for needRescan && iteration < maxIterations {
		needRescan = false
		iteration++

		// Шаг 2.3.1: Перестраиваем карту пересечений после предыдущих исправлений
		// ------------------------------------------------------------------------
		clusterToFiles = make(map[int][]string)
		for _, file := range f.files {
			if file.StartCluster < CLUSTER_DATA_START || file.StartCluster >= len(f.fatTable) {
				continue
			}
			chain := f.getChain(file.StartCluster)
			unique := make(map[int]bool)
			for _, c := range chain {
				unique[c] = true
			}
			for c := range unique {
				clusterToFiles[c] = append(clusterToFiles[c], file.Name)
			}
		}

		// Шаг 2.3.2: Находим и исправляем все текущие пересечения
		// --------------------------------------------------------
		for cluster, files := range clusterToFiles {
			// Пропускаем кластеры, которые используются только одним файлом
			if len(files) <= 1 {
				continue
			}

			// Выбираем файл-владелец (первый в списке)
			// Он остается без изменений, его цепочка считается "оригинальной"
			ownerFile := files[0]

			// Обрабатываем каждый конфликтующий файл (кроме владельца)
			for i := 1; i < len(files); i++ {
				conflictFile := files[i]

				// Пропускаем уже исправленные файлы
				if fixedFiles[conflictFile] {
					continue
				}

				// Получаем цепочку конфликтного файла
				conflictChain := f.getChain(fileInfo[conflictFile].StartCluster)

				// Структура для хранения информации о перенаправлениях
				// redirectCluster - кластер, который указывает на чужой кластер
				// targetCluster - кластер, на который указывает redirectCluster (принадлежит другому файлу)
				type Redirect struct {
					redirectCluster int
					targetCluster   int
				}
				var redirects []Redirect

				// Находим ВСЕ ссылки в конфликтном файле, которые ведут на чужие кластеры
				for _, c := range conflictChain {
					nextCluster := int(f.fatTable[c])
					// Проверяем, что следующий кластер валидный
					if nextCluster >= CLUSTER_DATA_START && nextCluster < len(f.fatTable) {
						// Проверяем, принадлежит ли следующий кластер другому файлу
						if filesList, exists := clusterToFiles[nextCluster]; exists {
							for _, fname := range filesList {
								if fname != conflictFile {
									// Нашли пересечение: кластер c указывает на кластер, принадлежащий fname
									redirects = append(redirects, Redirect{
										redirectCluster: c,
										targetCluster:   nextCluster,
									})
									break
								}
							}
						}
					}
				}

				// Если нет пересечений, переходим к следующему файлу
				if len(redirects) == 0 {
					continue
				}

				// Группируем перенаправления по целевым кластерам
				// Для каждого уникального целевого кластера запоминаем, какому файлу он принадлежит
				targetInfo := make(map[int]struct {
					targetCluster int
					targetFile    string
				})

				for _, r := range redirects {
					for _, fname := range clusterToFiles[r.targetCluster] {
						if fname != conflictFile {
							targetInfo[r.targetCluster] = struct {
								targetCluster int
								targetFile    string
							}{r.targetCluster, fname}
							break
						}
					}
				}

				// Для каждого уникального целевого файла копируем его цепочку
				for targetCluster, info := range targetInfo {
					targetFile := info.targetFile

					// Находим цепочку целевого файла
					targetChain := f.getChain(fileInfo[targetFile].StartCluster)

					// Находим позицию targetCluster в цепочке целевого файла
					startPos := -1
					for pos, c := range targetChain {
						if c == targetCluster {
							startPos = pos
							break
						}
					}

					if startPos == -1 {
						continue
					}

					// ВАЖНО: Копируем ВСЮ цепочку от targetCluster до конца для предотвращения двойных пересечений
					// Если скопировать только один кластер, оставшиеся кластеры могут
					// продолжать указывать на чужую цепочку, создавая новые пересечения
					chainToCopy := targetChain[startPos:]

					// Ищем свободные кластеры для копирования
					freeClusters := f.findFreeClusters(len(chainToCopy))

					if len(freeClusters) >= len(chainToCopy) {
						// Копируем данные всех кластеров
						if err := f.copyClusterData(chainToCopy, freeClusters); err == nil {
							// Связываем новые кластеры в цепочку
							// Создаем связи между новыми кластерами
							for j := 0; j < len(freeClusters)-1; j++ {
								f.fatTable[freeClusters[j]] = uint16(freeClusters[j+1])
							}
							// Последний новый кластер помечаем как EOF
							f.fatTable[freeClusters[len(freeClusters)-1]] = FAT_EOF

							// Перенаправляем все ссылки на новую цепочку
							for _, r := range redirects {
								if r.targetCluster == targetCluster {
									oldValue := f.fatTable[r.redirectCluster]
									// Перенаправляем на начало новой цепочки
									f.fatTable[r.redirectCluster] = uint16(freeClusters[0])

									// Записываем исправление в лог
									fixes = append(fixes, Damage{
										Type: "fix_intersection",
										Description: fmt.Sprintf("Исправлено пересечение: файл '%s' -> '%s'",
											conflictFile, ownerFile),
										Cluster:  r.redirectCluster,
										OldValue: oldValue,
										NewValue: uint16(freeClusters[0]),
									})
								}
							}

							// Освобождаем старые кластеры (если они не используются)
							for _, c := range chainToCopy {
								usedByOthers := false

								// Проверяем, используется ли кластер другими файлами
								for otherFile, otherChain := range fileChains {
									if otherFile == conflictFile || otherFile == targetFile {
										continue
									}
									for _, oc := range otherChain {
										if oc == c {
											usedByOthers = true
											break
										}
									}
									if usedByOthers {
										break
									}
								}

								// Проверяем, не указывает ли кто-то на этот кластер
								if !usedByOthers {
									for _, fc := range f.fatTable {
										if int(fc) == c {
											usedByOthers = true
											break
										}
									}
								}

								// Если кластер никем не используется, освобождаем его
								if !usedByOthers {
									f.fatTable[c] = FAT_FREE
								}
							}
						}
					} else {
						// Недостаточно свободных кластеров - обрезаем цепочку
						for _, r := range redirects {
							if r.targetCluster == targetCluster {
								oldValue := f.fatTable[r.redirectCluster]
								// Обрываем ссылку, помечая кластер как EOF
								f.fatTable[r.redirectCluster] = FAT_EOF
								fixes = append(fixes, Damage{
									Type: "fix_intersection",
									Description: fmt.Sprintf("Исправлено пересечение: файл '%s' обрезан на кластере %d (пересечение в кластере %d, недостаточно места для копирования %d кластеров)",
										conflictFile, r.redirectCluster, cluster, len(chainToCopy)),
									Cluster:  r.redirectCluster,
									OldValue: oldValue,
									NewValue: FAT_EOF,
								})
							}
						}
					}
				}

				// Отмечаем файл как исправленный
				fixedFiles[conflictFile] = true
				// Требуем повторного сканирования, так как изменения могли создать новые пересечения
				needRescan = true
			}
		}
	}

	// Сохраняем изменения FAT таблицы на диск
	f.saveFATTable()

	// =========================================
	// PART 3: НАХОЖДЕНИЕ И ИСПРАВЛЕНИЕ ЦИКЛОВ
	// =========================================
	// Цикл возникает, когда в цепочке кластеров встречается уже посещенный кластер
	for _, file := range f.files {
		// Пропускаем файлы с некорректным стартовым кластером
		if file.StartCluster < CLUSTER_DATA_START || file.StartCluster >= len(f.fatTable) {
			continue
		}

		// Получаем цепочку кластеров файла
		chain := f.getChain(file.StartCluster)

		// visited: отслеживает позицию каждого кластера в цепочке
		visited := make(map[int]int)
		hasLoop := false
		loopStart := -1

		// Проходим по цепочке в поисках повторяющегося кластера
		for i, c := range chain {
			if prev, ok := visited[c]; ok {
				// Нашли цикл: кластер c уже встречался на позиции prev
				hasLoop = true
				loopStart = prev
				break
			}
			visited[c] = i
		}

		if hasLoop {
			// Записываем информацию о цикле в результат
			loops = append(loops, Loop{
				StartCluster: file.StartCluster,
				FileName:     file.Name,
			})

			// Исправляем цикл: разрываем его, устанавливая EOF на предшествующем кластере
			if loopStart >= 0 && loopStart < len(chain)-1 {
				toFix := chain[loopStart]
				oldValue := f.fatTable[toFix]
				// Устанавливаем EOF, чтобы цепочка оборвалась
				f.fatTable[toFix] = FAT_EOF
				fixes = append(fixes, Damage{
					Type:        "fix_loop",
					Description: fmt.Sprintf("Исправлен цикл: файл %s, кластер %d", file.Name, toFix),
					Cluster:     toFix,
					OldValue:    oldValue,
					NewValue:    FAT_EOF,
				})
			}
		}
	}

	// Сохраняем финальные изменения FAT таблицы на диск
	f.saveFATTable()

	// Возвращаем все найденные проблемы и выполненные исправления
	return missingEOF, intersections, loops, fixes, nil
}

// GetFilesJSON возвращает список файлов в JSON
func (f *FAT16Instance) GetFilesJSON() ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return json.Marshal(f.files)
}

// GetFATTableJSON возвращает FAT таблицу в JSON
func (f *FAT16Instance) GetFATTableJSON() ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return json.Marshal(f.fatTable)
}

// Close закрывает экземпляр
func (f *FAT16Instance) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.file != nil {
		return f.file.Close()
	}
	return nil
}

//export CreateFAT16
func CreateFAT16() C.int {
	instMu.Lock()
	defer instMu.Unlock()
	id := nextID
	nextID++
	instances[id] = &FAT16Instance{
		fatTable: make([]uint16, 0),
		files:    make([]FileInfo, 0),
	}
	return C.int(id)
}

//export LoadFAT16
func LoadFAT16(filename *C.char) C.int {
	instMu.Lock()
	defer instMu.Unlock()

	goFilename := C.GoString(filename)
	inst, err := loadFAT16(goFilename)
	if err != nil {
		return C.int(-1)
	}

	id := nextID
	nextID++
	instances[id] = inst
	return C.int(id)
}

//export GetFiles
func GetFiles(id C.int, buffer *C.char, bufferSize C.int) C.int {
	instMu.Lock()
	inst, ok := instances[int(id)]
	instMu.Unlock()

	if !ok {
		return C.int(-1)
	}

	data, err := inst.GetFilesJSON()
	if err != nil {
		return C.int(-3)
	}

	if len(data) > int(bufferSize)-1 {
		return C.int(-2)
	}

	copy((*[1 << 30]byte)(unsafe.Pointer(buffer))[:len(data)], data)
	return C.int(len(data))
}

//export GetFATTable
func GetFATTable(id C.int, buffer *C.char, bufferSize C.int) C.int {
	instMu.Lock()
	inst, ok := instances[int(id)]
	instMu.Unlock()

	if !ok {
		return C.int(-1)
	}

	data, err := inst.GetFATTableJSON()
	if err != nil {
		return C.int(-3)
	}

	if len(data) > int(bufferSize)-1 {
		return C.int(-2)
	}

	copy((*[1 << 30]byte)(unsafe.Pointer(buffer))[:len(data)], data)
	return C.int(len(data))
}

//export CreateDamage
func CreateDamage(id C.int, damageType *C.char, result *C.char, bufferSize C.int) C.int {
	instMu.Lock()
	inst, ok := instances[int(id)]
	instMu.Unlock()

	if !ok {
		return C.int(-1)
	}

	dt := C.GoString(damageType)
	var damage *Damage
	var err error

	switch dt {
	case "missing_eof":
		damage, err = inst.CreateMissingEOF()
	case "intersection":
		damage, err = inst.CreateIntersection()
	case "loop":
		damage, err = inst.CreateLoop()
	default:
		return C.int(-3)
	}

	if err != nil {
		return C.int(-3)
	}

	data, err := json.Marshal(damage)
	if err != nil {
		return C.int(-3)
	}

	if len(data) > int(bufferSize)-1 {
		return C.int(-2)
	}

	copy((*[1 << 30]byte)(unsafe.Pointer(result))[:len(data)], data)
	return C.int(len(data))
}

//export CheckFAT
func CheckFAT(id C.int, result *C.char, bufferSize C.int) C.int {
	instMu.Lock()
	inst, ok := instances[int(id)]
	instMu.Unlock()

	if !ok {
		return C.int(-1)
	}

	missingEOF, intersections, loops, fixes, err := inst.CheckAndFix()
	if err != nil {
		return C.int(-3)
	}

	response := struct {
		MissingEOF    []int          `json:"missing_eof"`
		Intersections []Intersection `json:"intersections"`
		Loops         []Loop         `json:"loops"`
		Fixes         []Damage       `json:"fixes"`
	}{
		MissingEOF:    missingEOF,
		Intersections: intersections,
		Loops:         loops,
		Fixes:         fixes,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return C.int(-3)
	}

	if len(data) > int(bufferSize)-1 {
		return C.int(-2)
	}

	copy((*[1 << 30]byte)(unsafe.Pointer(result))[:len(data)], data)
	return C.int(len(data))
}

//export CloseFAT16
func CloseFAT16(id C.int) {
	instMu.Lock()
	defer instMu.Unlock()

	if inst, ok := instances[int(id)]; ok {
		inst.Close()
		delete(instances, int(id))
	}
}

//export GetClusterChain
func GetClusterChain(id C.int, startCluster C.int, buffer *C.char, bufferSize C.int) C.int {
	instMu.Lock()
	inst, ok := instances[int(id)]
	instMu.Unlock()

	if !ok {
		return C.int(-1)
	}

	inst.mu.RLock()
	defer inst.mu.RUnlock()

	chain := inst.getChain(int(startCluster))

	// Преобразуем в JSON
	data, err := json.Marshal(chain)
	if err != nil {
		return C.int(-3)
	}

	if len(data) > int(bufferSize)-1 {
		return C.int(-2)
	}

	copy((*[1 << 30]byte)(unsafe.Pointer(buffer))[:len(data)], data)
	return C.int(len(data))
}

func main() {}
