interface BasicMsg {
	type: string
}

type BatchMsg = BasicMsg[]

interface PingMsg {
	type: 'ping'
	data?: any
}

interface ErrorMsg {
	type: 'error'
	message: string
}

type LogLevel = 'DBUG' | 'INFO' | 'WARN' | 'ERRO'

export interface LogMsg {
	type: 'log'
	time: number
	lvl: LogLevel
	log: string
}

export class LogIO {
	private ws: WebSocket | null = null
	private logListener: ((msg: LogMsg) => void)[] = []
	private closeListener: ((err?: unknown) => void)[] = []

	constructor(ws: WebSocket) {
		this.setWs(ws)
	}

	private setWs(ws: WebSocket): void {
		ws.addEventListener('close', () => this.onClose())
		ws.addEventListener('message', (msg) => {
			const res = JSON.parse(msg.data) as BasicMsg | BatchMsg
			if (Array.isArray(res)) {
				for (const r of res) {
					this.onMessage(r)
				}
			} else {
				this.onMessage(res)
			}
		})
		this.ws = ws
	}

	get isActive(): boolean {
		return !!this.ws && this.ws.readyState === WebSocket.OPEN
	}

	close(): void {
		if (this.ws) {
			this.onClose()
			this.ws.close()
			this.ws = null
		}
	}

	private onError(err: unknown): void {
		if (!this.ws) {
			return
		}
		for (const l of this.closeListener) {
			l(err)
		}
	}

	private onClose(): void {
		if (!this.ws) {
			return
		}
		for (const l of this.closeListener) {
			l()
		}
	}

	private onMessage(msg: BasicMsg): void {
		switch (msg.type) {
			case 'ping':
				this.ws?.send(
					JSON.stringify({
						type: 'pong',
						data: (msg as PingMsg).data,
					}),
				)
				break
			case 'error':
				if (this.ws) {
					this.onError(msg)
					this.ws.close()
					this.ws = null
				}
				break
			case 'log':
				this.onLog(msg as LogMsg)
				break
		}
	}

	private onLog(msg: LogMsg): void {
		for (const l of this.logListener) {
			l(msg)
		}
	}

	setLevel(lvl: LogLevel): void {
		this.ws?.send(
			JSON.stringify({
				type: 'set-level',
				level: lvl,
			}),
		)
	}

	addLogListener(l: (msg: LogMsg) => void): void {
		this.logListener.push(l)
	}

	addCloseListener(l: () => void): void {
		this.closeListener.push(l)
		console.debug('putted close listener', this.closeListener)
	}

	static async dial(token: string): Promise<LogIO> {
		const wsTarget = `${httpToWs(window.location.protocol)}//${window.location.host}/api/v0/log.io`
		const ws = new WebSocket(wsTarget)

		var connTimeout: ReturnType<typeof setTimeout>
		await new Promise<void>((resolve, reject) => {
			connTimeout = setTimeout(() => {
				reject('WebSocket dial timeout')
				ws.close()
			}, 1000 * 15)
			ws.addEventListener('error', reject)
			ws.addEventListener('open', () => {
				ws.removeEventListener('error', reject)
				resolve()
			})
		}).finally(() => clearTimeout(connTimeout))

		var after: () => void
		await new Promise<void>((resolve, reject) => {
			const listener = (msg: MessageEvent) => {
				console.debug('log.io auth result:', msg.data)
				try {
					const res = JSON.parse(msg.data) as BasicMsg
					if (res.type === 'error') {
						reject((res as ErrorMsg).message)
					} else if (res.type === 'ready') {
						resolve()
					}
				} catch (err) {
					reject(err)
				}
			}
			ws.addEventListener('message', listener)
			after = () => ws.removeEventListener('message', listener)
			ws.send(
				JSON.stringify({
					token: token,
				}),
			)
		}).finally(() => after())

		return new LogIO(ws)
	}
}

function httpToWs(protocol: string): string {
	return protocol == 'http:' ? 'ws:' : 'wss:'
}
