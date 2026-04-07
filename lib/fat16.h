#ifndef FAT16_H
#define FAT16_H

#ifdef __cplusplus
extern "C" {
#endif

// Создает новый пустой экземпляр FAT16
int CreateFAT16();

// Загружает образ FAT16 из файла
int LoadFAT16(const char* filename);

// Получает FAT таблицу в JSON формате
int GetFATTable(int id, char* buffer, int bufferSize);

// Получает список файлов в JSON формате
int GetFiles(int id, char* buffer, int bufferSize);

// Получает цепочку кластеров для файла
int GetClusterChain(int id, int startCluster, char* buffer, int bufferSize);

// Создает повреждение в FAT таблице
int CreateDamage(int id, const char* damageType, char* result, int bufferSize);

// Проверяет и исправляет файловую систему
int CheckFAT(int id, char* result, int bufferSize);

// Закрывает экземпляр FAT16
void CloseFAT16(int id);

#ifdef __cplusplus
}
#endif

#endif