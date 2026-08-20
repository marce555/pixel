import sys

def main():
    try:
        query = sys.argv[1]
        print(f'Result for {query}')
    except Exception as e:
        print(f'Error: {e}')

if __name__ == '__main__':
    main()