import Foundation

struct Subscription: Codable, Identifiable, Sendable {
    let id: String
    let file: String
    let provider: String
    let models: [Model]

    enum CodingKeys: String, CodingKey {
        case id = "_id"
        case file
        case provider
        case models
    }

    init(file: String, provider: String, models: [Model]) {
        self.id = file
        self.file = file
        self.provider = provider
        self.models = models
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        self.file = try container.decode(String.self, forKey: .file)
        self.provider = try container.decode(String.self, forKey: .provider)
        self.models = try container.decode([Model].self, forKey: .models)
        self.id = self.file
    }
}

struct Model: Codable, Identifiable, Sendable {
    let id: String
    let displayName: String?
    let ownedBy: String?

    enum CodingKeys: String, CodingKey {
        case id
        case displayName = "display_name"
        case ownedBy = "owned_by"
    }
}

struct ModelsResponse: Codable, Sendable {
    let data: [Model]
}

@MainActor
@Observable
final class APIClient {
    private(set) var subscriptions: [Subscription] = []
    private(set) var exposedModels: [String] = []
    private(set) var isLoading = false
    private(set) var lastError: String?

    func fetchSubscriptions(baseURL: URL, secret: String) async {
        isLoading = true
        lastError = nil
        do {
            let url = baseURL.appendingPathComponent("v0/management/subscriptions")
            let request = authorizedRequest(url: url, secret: secret)
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            let decoded = try JSONDecoder().decode(SubscriptionsResponse.self, from: data)
            subscriptions = decoded.subscriptions
        } catch {
            lastError = error.localizedDescription
        }
        isLoading = false
    }

    func fetchExposedModels(baseURL: URL, secret: String) async {
        do {
            let url = baseURL.appendingPathComponent("v0/management/exposed-models")
            let request = authorizedRequest(url: url, secret: secret)
            let (data, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            let decoded = try JSONDecoder().decode(ExposedModelsResponse.self, from: data)
            exposedModels = decoded.models
        } catch {
            lastError = error.localizedDescription
        }
    }

    func setExposedModels(baseURL: URL, secret: String, models: [String]) async {
        do {
            let url = baseURL.appendingPathComponent("v0/management/exposed-models")
            var request = authorizedRequest(url: url, secret: secret)
            request.httpMethod = "PUT"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            let body = try JSONEncoder().encode(["models": models])
            request.httpBody = body
            let (_, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            exposedModels = models
        } catch {
            lastError = error.localizedDescription
        }
    }

    func extractAuth(baseURL: URL, secret: String) async {
        isLoading = true
        lastError = nil
        do {
            let url = baseURL.appendingPathComponent("v0/management/extract-auth")
            var request = authorizedRequest(url: url, secret: secret)
            request.httpMethod = "POST"
            let (_, response) = try await URLSession.shared.data(for: request)
            guard let http = response as? HTTPURLResponse, http.statusCode == 200 else {
                throw URLError(.badServerResponse)
            }
            await fetchSubscriptions(baseURL: baseURL, secret: secret)
            await fetchExposedModels(baseURL: baseURL, secret: secret)
        } catch {
            lastError = error.localizedDescription
        }
        isLoading = false
    }

    private func authorizedRequest(url: URL, secret: String) -> URLRequest {
        var request = URLRequest(url: url)
        request.setValue(secret, forHTTPHeaderField: "X-Management-Key")
        return request
    }
}

private struct SubscriptionsResponse: Codable, Sendable {
    let subscriptions: [Subscription]
}

private struct ExposedModelsResponse: Codable, Sendable {
    let models: [String]
}
