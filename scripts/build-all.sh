#!/bin/bash

echo "=== Сборка FAT16 Analyzer ==="
echo "Текущая директория: $(pwd)"

# Функция для вывода ошибок
error_exit() {
    echo "❌ $1"
    exit 1
}

# Удаляем старые файлы
echo ""
echo "🧹 Очистка..."
rm -f libfat16.so fat16.h fat16-server fat16-client
rm -f libfat16.h 2>/dev/null
rm -f lib/go.sum server/go.sum client/go.sum 2>/dev/null
echo "✅ Очистка завершена"

# Обновляем зависимости lib
echo ""
echo "📦 Обновление зависимостей lib..."
cd lib || error_exit "директория lib не найдена"
go mod tidy
if [ $? -ne 0 ]; then
    error_exit "Ошибка обновления зависимостей lib"
fi
echo "✅ Зависимости lib обновлены"
cd ..

# Обновляем зависимости server
echo ""
echo "📦 Обновление зависимостей server..."
cd server || error_exit "директория server не найдена"
go mod tidy
if [ $? -ne 0 ]; then
    error_exit "Ошибка обновления зависимостей server"
fi
echo "✅ Зависимости server обновлены"
cd ..

# Обновляем зависимости client
echo ""
echo "📦 Обновление зависимостей client..."
cd client || error_exit "директория client не найдена"
go mod tidy
if [ $? -ne 0 ]; then
    error_exit "Ошибка обновления зависимостей client"
fi
echo "✅ Зависимости client обновлены"
cd ..

# Собираем библиотеку
echo ""
echo "🔨 Шаг 1: Сборка SO библиотеки"
cd lib || error_exit "директория lib не найдена"

# Собираем shared library
go build -buildmode=c-shared -ldflags="-w -s" -o ../libfat16.so .
if [ $? -ne 0 ]; then
    error_exit "Ошибка сборки библиотеки"
fi
echo "✅ Библиотека успешно собрана"

cd ..

# Копируем наш заголовочный файл (не автоматически сгенерированный)
cp lib/fat16.h .
if [ ! -f "fat16.h" ]; then
    error_exit "fat16.h не скопирован"
fi
echo "✅ Заголовочный файл fat16.h скопирован"

# Удаляем автоматически сгенерированный заголовок, если он есть
rm -f libfat16.h

# Устанавливаем LD_LIBRARY_PATH
export LD_LIBRARY_PATH=$(pwd):$LD_LIBRARY_PATH
echo "✅ LD_LIBRARY_PATH установлен: $LD_LIBRARY_PATH"

# Сборка сервера
echo ""
echo "🔨 Шаг 2: Сборка сервера"
cd server || error_exit "директория server не найдена"

# Собираем сервер
go build -ldflags="-w -s" -o ../fat16-server .
if [ $? -ne 0 ]; then
    error_exit "Ошибка сборки сервера"
fi
echo "✅ Сервер успешно собран"

cd ..

# Сборка клиента
echo ""
echo "🔨 Шаг 3: Сборка клиента"
cd client || error_exit "директория client не найдена"

# Собираем клиент
go build -ldflags="-w -s" -o ../fat16-client .
if [ $? -ne 0 ]; then
    error_exit "Ошибка сборки клиента"
fi
echo "✅ Клиент успешно собран"

cd ..

# Проверка
echo ""
echo "📋 Проверка собранных файлов:"
ls -la libfat16.so fat16.h fat16-server fat16-client 2>/dev/null

echo ""
echo "=== Сборка завершена! ==="
echo "Для запуска:"
echo "  ./run.sh server 8080    # в одном терминале"
echo "  ./run.sh client         # в другом терминале"