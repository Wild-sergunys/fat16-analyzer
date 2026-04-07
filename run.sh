#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export LD_LIBRARY_PATH="$SCRIPT_DIR:$LD_LIBRARY_PATH"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Проверка наличия библиотеки
if [ ! -f "$SCRIPT_DIR/libfat16.so" ]; then
    echo -e "${RED}❌ Библиотека libfat16.so не найдена!${NC}"
    echo "Сначала выполните: ./scripts/build-all.sh"
    exit 1
fi

# Проверка наличия заголовочного файла
if [ ! -f "$SCRIPT_DIR/fat16.h" ]; then
    echo -e "${RED}❌ Заголовочный файл fat16.h не найден!${NC}"
    echo "Сначала выполните: ./scripts/build-all.sh"
    exit 1
fi

case "$1" in
    server)
        PORT=${2:-8080}
        echo -e "${GREEN}🚀 Запуск сервера на порту ${PORT}...${NC}"
        echo -e "${YELLOW}LD_LIBRARY_PATH=${LD_LIBRARY_PATH}${NC}"
        if [ -f "$SCRIPT_DIR/fat16-server" ]; then
            cd "$SCRIPT_DIR"
            ./fat16-server "$PORT"
        else
            echo -e "${RED}❌ Исполняемый файл fat16-server не найден!${NC}"
            echo "Сначала выполните: ./scripts/build-all.sh"
            exit 1
        fi
        ;;
    client)
        echo -e "${GREEN}🖥️  Запуск клиента...${NC}"
        echo -e "${YELLOW}ℹ️  Порт можно будет указать в графическом интерфейсе${NC}"
        if [ -f "$SCRIPT_DIR/fat16-client" ]; then
            cd "$SCRIPT_DIR"
            ./fat16-client
        else
            echo -e "${RED}❌ Исполняемый файл fat16-client не найден!${NC}"
            echo "Сначала выполните: ./scripts/build-all.sh"
            exit 1
        fi
        ;;
    create)
        echo -e "${GREEN}📀 Создание тестового образа FAT16...${NC}"
        ./scripts/create_fat16.sh
        ;;
    clean)
        echo -e "${YELLOW}🧹 Очистка...${NC}"
        cd "$SCRIPT_DIR"
        rm -f fat16-server fat16-client libfat16.so fat16.h libfat16.h
        rm -f fat16.img 2>/dev/null
        echo -e "${GREEN}✅ Очистка завершена${NC}"
        ;;
    *)
        echo "Использование: $0 {server|client|create|clean} [порт]"
        echo ""
        echo "Команды:"
        echo "  server [порт]  - запустить сервер (порт по умолчанию: 8080)"
        echo "  client         - запустить клиент (порт выбирается в GUI)"
        echo "  create         - создать тестовый образ FAT16"
        echo "  clean          - очистить скомпилированные файлы"
        echo ""
        echo "Примеры:"
        echo "  $0 create              - создать тестовый образ"
        echo "  $0 server 8080         - запуск сервера"
        echo "  $0 client              - запуск клиента"
        exit 1
        ;;
esac